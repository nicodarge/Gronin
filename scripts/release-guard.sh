#!/usr/bin/env bash
# The release job's decisions: `tip`, `repair` and `newest` read the environment; `--self-test` runs them.
set -euo pipefail

# Must stay in step with `message` in .releaserc (the self-test checks it).
RELEASE_SUBJECT_RE='^chore\(release\): [0-9]+\.[0-9]+\.[0-9]+ \[skip ci\]$'
TAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'

fail() {
    echo "::error::$*" >&2
    exit 1
}

need() {
    local name
    for name in "$@"; do
        [ -n "${!name:-}" ] || fail "${name} is not set"
    done
}

# Call as a plain statement: inside `if classify` a failing git would read as "not a release commit".
subject=""
is_release=0
classify() {
    local files
    subject="$(git log -1 --format=%s "$1")"
    files="$(git diff-tree --no-commit-id --name-only -r "$1")"
    is_release=0
    if [[ "${subject}" =~ ${RELEASE_SUBJECT_RE} ]] && [ "${files}" = "CHANGELOG.md" ]; then
        is_release=1
    fi
}

# A tag counts only on the release commit of its own version, so a hand-made tag cannot pass.
tag_ok=0
classify_tag() {
    classify "refs/tags/$1^{commit}"
    tag_ok=0
    if [ "${is_release}" = 1 ] && [ "${subject}" = "chore(release): ${1#v} [skip ci]" ]; then
        tag_ok=1
    fi
}

newest=""
newest_release_tag() {
    local tags candidate
    newest=""
    tags="$(git tag --list 'v*' --merged "$1" --sort=-version:refname)"
    while read -r candidate; do
        [[ "${candidate}" =~ ${TAG_RE} ]] || continue
        classify_tag "${candidate}"
        if [ "${tag_ok}" = 1 ]; then
            newest="${candidate}"
            break
        fi
    done <<< "${tags}"
}

# The checkout is as old as the job; production may have moved since.
sync_production() {
    git fetch --prune --prune-tags --tags --force origin
    git merge --ff-only --quiet origin/production
}

cmd_tip() {
    local head_sha="${HEAD_SHA:-}" commits sha green ungated=""
    need EVENT_NAME GITHUB_OUTPUT
    case "${EVENT_NAME}" in
        workflow_run) [[ "${head_sha}" =~ ^[0-9a-f]{40}$ ]] || fail "HEAD_SHA is not a 40-character commit hash" ;;
        workflow_dispatch) need GITHUB_REPOSITORY ;;
        *) fail "EVENT_NAME is neither workflow_run nor workflow_dispatch" ;;
    esac
    sync_production

    if [ "${EVENT_NAME}" = "workflow_dispatch" ]; then
        head_sha=""
        commits="$(git rev-list --max-count=100 HEAD)"
        while read -r sha; do
            classify "${sha}"
            if [ "${is_release}" = 0 ]; then
                head_sha="${sha}"
                break
            fi
        done <<< "${commits}"
        [ -n "${head_sha}" ] || fail "no commit that is not a release commit in the last 100 of production"
        green="$(gh api "repos/${GITHUB_REPOSITORY}/commits/${head_sha}/check-runs?check_name=gate&per_page=100" \
            --jq '[.check_runs[] | select(.app.slug == "github-actions")] | sort_by(.started_at) | last | select(.status == "completed" and .conclusion == "success") | .id')"
        [ -n "${green}" ] || fail "no successful gate check run from GitHub Actions on ${head_sha}"
    fi

    git merge-base --is-ancestor "${head_sha}" HEAD || fail "${head_sha} is not an ancestor of production"

    commits="$(git rev-list "${head_sha}..HEAD")"
    while read -r sha; do
        [ -n "${sha}" ] || continue
        classify "${sha}"
        [ "${is_release}" = 1 ] || ungated="${ungated} ${sha}"
    done <<< "${commits}"

    if [ -n "${ungated}" ]; then
        echo "::notice::production moved past ${head_sha}; the run for its tip releases it"
        echo "current=false" >> "${GITHUB_OUTPUT}"
    else
        echo "current=true" >> "${GITHUB_OUTPUT}"
    fi
}

cmd_repair() {
    local tag="${TAG:-}" latest=false state
    need GITHUB_REPOSITORY GITHUB_OUTPUT
    [[ "${tag}" =~ ${TAG_RE} ]] || fail "the tag input is not of the form vMAJOR.MINOR.PATCH"
    sync_production

    git rev-parse --verify --quiet "refs/tags/${tag}^{commit}" > /dev/null || fail "tag ${tag} does not exist"
    git merge-base --is-ancestor "refs/tags/${tag}" origin/production || fail "tag ${tag} is not an ancestor of production"
    classify_tag "${tag}"
    [ "${tag_ok}" = 1 ] || fail "tag ${tag} does not point at the release commit semantic-release made for it"

    newest_release_tag origin/production
    [ "${newest}" = "${tag}" ] && latest=true

    # `gh release view` does not find a draft, and creating over one makes a second release.
    state="$(gh release list --repo "${GITHUB_REPOSITORY}" --limit 1000 --json tagName,isDraft |
        jq -r --arg tag "${tag}" '.[] | select(.tagName == $tag) | .isDraft')"
    [ "${state}" != "true" ] || fail "the release of ${tag} is a draft: publish or delete it, then run this again"
    if [ -z "${state}" ]; then
        gh release create "${tag}" --repo "${GITHUB_REPOSITORY}" --verify-tag --generate-notes --latest="${latest}"
    fi
    echo "tag=${tag}" >> "${GITHUB_OUTPUT}"
}

cmd_newest() {
    local tag="${TAG:-}"
    need TAG GITHUB_OUTPUT
    git fetch --tags --force origin '+refs/heads/production:refs/remotes/origin/production'
    newest_release_tag origin/production
    if [ "${newest}" = "${tag}" ]; then
        echo "latest=true" >> "${GITHUB_OUTPUT}"
    else
        echo "latest=false" >> "${GITHUB_OUTPUT}"
    fi
}

# ---- self-test -------------------------------------------------------------------------

self_test() {
    local self root n_fail=0 n_ok=0
    self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
    root="$(cd "$(dirname "$0")/.." && pwd)"
    t="$(mktemp -d)"
    trap 'rm -rf "${t}"' EXIT

    unset "${!GIT_@}" 2> /dev/null || true
    export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
    export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
    export GH_LOG="${t}/gh.log" GH_FIXTURES="${t}/fix" GH_RELEASES="${t}/rel"
    export REAL_GIT
    REAL_GIT="$(command -v git)"
    mkdir -p "${t}/bin" "${t}/bin-fail" "${t}/fix"

    # `gh` stub: the real jq filter runs on the real response shape; GH_MODE makes it fail or answer garbage.
    cat > "${t}/bin/gh" << 'STUB'
#!/usr/bin/env bash
echo "gh $*" >> "${GH_LOG}"
if [ "${GH_MODE:-}" = fail ]; then echo "HTTP 502: Bad Gateway" >&2; exit 1; fi
case "$1 $2" in
    "api repos/"*)
        sha="$(sed -E 's#.*/commits/([0-9a-f]+)/check-runs.*#\1#' <<< "$2")"
        name="$(sed -nE 's#.*[?&]check_name=([^&]*).*#\1#p' <<< "$2")"
        jqf=""
        while [ $# -gt 0 ]; do [ "$1" = --jq ] && jqf="$2"; shift; done
        f="${GH_FIXTURES}/${sha}.json"
        [ -f "$f" ] || f="${GH_FIXTURES}/default.json"
        case "${GH_MODE:-}" in
            garbage) echo 'not json' ;;
            object) echo '{"message":"API rate limit exceeded"}' ;;
            *) jq --arg n "${name}" 'if $n == "" then . else .check_runs |= map(select(.name == $n)) end' "$f" ;;
        esac | jq -r "${jqf}" ;;
    "release list")
        case "${GH_MODE:-}" in
            garbage) echo 'not json' ;;
            object) echo '{"message":"API rate limit exceeded"}' ;;
            *)
                for f in "${GH_RELEASES}"/*; do
                    [ -f "$f" ] || continue
                    jq -n --arg t "$(basename "$f")" --argjson d "$(cat "$f")" '{tagName:$t,isDraft:$d}'
                done | jq -s . ;;
        esac ;;
    "release create") echo false > "${GH_RELEASES}/$3" ;;
    *) echo "stub gh: unexpected $*" >&2; exit 99 ;;
esac
STUB
    # A git that fails on one subcommand, to show a failing git is an error and not "nothing ungated".
    cat > "${t}/bin-fail/git" << 'STUB'
#!/usr/bin/env bash
if [ "$1" = "${FAIL_GIT_SUBCOMMAND:-}" ]; then echo "fatal: injected failure of git $1" >&2; exit 128; fi
exec "${REAL_GIT}" "$@"
STUB
    chmod +x "${t}/bin/gh" "${t}/bin-fail/git"
    cat > "${t}/fix/default.json" << 'JSON'
{"total_count":2,"check_runs":[{"name":"gate","status":"completed","conclusion":"success","started_at":"2026-09-15T07:18:48Z","app":{"slug":"github-actions"}},{"name":"build","status":"completed","conclusion":"failure","started_at":"2026-09-15T07:19:48Z","app":{"slug":"github-actions"}}]}
JSON

    commit() { echo "${RANDOM}${RANDOM}" >> "$2"; git add "$2"; git commit -q -m "$1"; }
    relc() { commit "chore(release): $1 [skip ci]" CHANGELOG.md; git tag "v$1"; }
    mkrepo() {
        cd "${t}"
        rm -rf "${t}/w" "${t}/o.git" "${t}/run" "${t}/rel"
        mkdir -p "${t}/rel"
        git init -q --bare -b production "${t}/o.git"
        git clone -q "${t}/o.git" "${t}/w" 2> /dev/null
        cd "${t}/w"
        git checkout -q -b production
        commit "feat: root" a.txt
    }
    publish() { git -C "${t}/w" push -q origin production --tags 2> /dev/null; }
    clone() { rm -rf "${t}/run"; git clone -q -b production "${t}/o.git" "${t}/run" 2> /dev/null; }
    gatefix() { jq --arg s "$1" "${2:-.}" "${t}/fix/default.json" > "${t}/fix/$1.json"; }
    sha() { git -C "${t}/w" rev-parse "$1"; }

    # run <subcommand> [VAR=val ...]: sets rc, out (GITHUB_OUTPUT) and log (stdout and stderr)
    rc=0 out="" log=""
    run() {
        local sub="$1"
        shift
        : > "${t}/out"
        : > "${GH_LOG}"
        rc=0
        (cd "${t}/run" && env GITHUB_OUTPUT="${t}/out" GITHUB_REPOSITORY=example/repo PATH="${t}/bin:${PATH}" "$@" bash "${self}" "${sub}") > "${t}/log" 2>&1 || rc=$?
        out="$(tr '\n' ' ' < "${t}/out" | sed 's/ $//')"
        log="$(cat "${t}/log")"
    }
    # check <label> <rc, or nz for any failure> <GITHUB_OUTPUT line, or empty for none> [text the log must contain]
    check() {
        local label="$1" want_rc="$2" want_out="$3" want_log="${4:-}" ok=1
        if [ "${want_rc}" = nz ]; then [ "${rc}" != 0 ] || ok=0; else [ "${rc}" = "${want_rc}" ] || ok=0; fi
        [ "${out}" = "${want_out}" ] || ok=0
        if [ -n "${want_log}" ] && ! grep -q -F -- "${want_log}" <<< "${log}"; then ok=0; fi
        if [ "${ok}" = 1 ]; then
            n_ok=$((n_ok + 1))
        else
            n_fail=$((n_fail + 1))
            echo "FAIL ${label}: rc=${rc} (want ${want_rc}), output=[${out}] (want [${want_out}]), log wants [${want_log}]" >&2
            echo "    | ${log//$'\n'/$'\n'    | }" >&2
        fi
    }
    gh_calls() { grep -c "^gh $1" "${GH_LOG}" || true; }
    refuses() { ! [[ "$1" =~ ${RELEASE_SUBJECT_RE} ]]; }
    accepts() { [[ "$1" =~ ${RELEASE_SUBJECT_RE} ]]; }
    subject_of() { printf '%s' "${1%%$'\n'*}"; } # a commit's subject is the first line only
    ensure() { # ensure <label> <condition command...>
        local label="$1"
        shift
        if "$@"; then n_ok=$((n_ok + 1)); else n_fail=$((n_fail + 1)); echo "FAIL ${label}" >&2; fi
    }

    # expect_failure <check|ensure> <args...>: the assertion must register a failure, or the harness cannot fail
    expect_failure() {
        local before=${n_fail}
        "$@" 2> /dev/null
        if [ "${n_fail}" -gt "${before}" ]; then
            n_fail=${before}
            n_ok=$((n_ok + 1))
        else
            n_fail=$((n_fail + 1))
            echo "FAIL the harness did not notice: $*" >&2
        fi
    }

    # .releaserc must produce subjects the pattern accepts, and refuse what it must refuse.
    local msg
    msg="$(jq -er '.plugins[] | select(type == "array" and .[0] == "@semantic-release/git") | .[1].message' "${root}/.releaserc" || true)"
    ensure ".releaserc has a git plugin message" test -n "${msg}"
    msg="${msg//\$\{nextRelease.version\}/1.2.3}"
    ensure ".releaserc message (${msg}) has a subject matching the release-subject pattern" accepts "$(subject_of "${msg}")"
    ensure "a subject followed by a body is judged on its first line" accepts "$(subject_of "chore(release): 1.2.3 [skip ci]"$'\n\n'"notes")"
    ensure "a message whose first line is not the pattern is refused even if a later line matches" refuses "$(subject_of "release"$'\n'"chore(release): 1.2.3 [skip ci]")"
    ensure "the pattern refuses a pre-release version" refuses "chore(release): 1.2.3-rc.1 [skip ci]"
    ensure "the pattern refuses a subject without the skip marker" refuses "chore(release): 1.2.3"
    ensure "the pattern refuses a squash suffix after the marker" refuses "chore(release): 1.2.3 [skip ci] (#9)"
    ensure "the pattern refuses a prefix before the subject" refuses "x chore(release): 1.2.3 [skip ci]"

    # tip, from a workflow_run payload
    mkrepo; commit "fix: one" a.txt; local p; p="$(sha HEAD)"; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: payload is HEAD" 0 "current=true"
    expect_failure check "harness: a wrong exit status is noticed" 1 "current=true"
    expect_failure check "harness: any failure is wanted but the run succeeded" nz "current=true"
    expect_failure check "harness: a wrong output is noticed" 0 "current=false"
    expect_failure check "harness: a missing log text is noticed" 0 "current=true" "text that is not in the log"
    expect_failure ensure "harness: a false condition is noticed" false

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; relc 1.0.0; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: only a release commit after the payload" 0 "current=true"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; relc 1.0.0; commit "fix: two" a.txt; relc 1.0.1; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: ungated commit after the payload" 0 "current=false" "production moved past"

    mkrepo; commit "fix: one" a.txt; git checkout -q -b side; commit "fix: side" b.txt; local s; s="$(sha HEAD)"
    git checkout -q production; commit "fix: two" a.txt; git push -q origin side 2> /dev/null; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${s}"; check "tip: payload not an ancestor" 1 "" "is not an ancestor"
    local bad
    p="$(sha production)"
    run tip EVENT_NAME=push HEAD_SHA="${p}"; check "tip: an event that is neither workflow_run nor workflow_dispatch" 1 "" "::error::EVENT_NAME is neither"
    run tip HEAD_SHA="${p}"; check "tip: no EVENT_NAME" 1 "" "::error::EVENT_NAME is not set"
    run tip EVENT_NAME=workflow_run; check "tip: no HEAD_SHA" 1 "" "::error::HEAD_SHA is not a 40-character"
    for bad in HEAD "${p:0:12}" "${p:0:39}" "${p}0" "${p^^}" "--all" "--$(printf '0%.0s' {1..38})" "${p};id" "${p} " "${p}"$'\n' "z${p}" "${p:0:20}"$'\n'"${p:20}" "${p}"$'\n::error::injected'; do
        run tip EVENT_NAME=workflow_run "HEAD_SHA=${bad}"; check "tip: HEAD_SHA $(printf '%q' "${bad}")" 1 "" "::error::HEAD_SHA is not a 40-character"
        ensure "tip: that value is not echoed: one line of output" test "$(grep -c . <<< "${log}")" = 1
        ensure "tip: that value is not echoed: one annotation" test "$(grep -c '^::' <<< "${log}")" = 1
    done
    run tip EVENT_NAME=$'workflow_run\n::error::injected' HEAD_SHA="${p}"; check "tip: EVENT_NAME with a newline" 1 "" "::error::EVENT_NAME is neither"
    ensure "tip: that value is not echoed either" test "$(grep -c '^::' <<< "${log}")" = 1
    run tip EVENT_NAME=workflow_dispatch GITHUB_REPOSITORY=; check "tip: a dispatch without GITHUB_REPOSITORY" 1 "" "::error::GITHUB_REPOSITORY is not set"
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}" GITHUB_OUTPUT=; check "tip: no GITHUB_OUTPUT" 1 "" "::error::GITHUB_OUTPUT is not set"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; commit "chore(release): 9.9.9 [skip ci]" src.go; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: release-titled commit that touches code" 0 "current=false"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; echo x >> CHANGELOG.md; echo y >> src.go; git add -A
    git commit -q -m "chore(release): 9.9.9 [skip ci]"; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: release-titled commit touching CHANGELOG.md and code" 0 "current=false"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; commit "docs: rewrite the changelog by hand" CHANGELOG.md; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: a commit touching only CHANGELOG.md is not a release commit without the subject" 0 "current=false"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"
    echo "${RANDOM}" >> CHANGELOG.md; git add CHANGELOG.md; git commit -q -m "chore(release): 1.0.0 [skip ci]" -m "notes under the subject"; git tag v1.0.0; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: a release commit with a body is judged on its subject" 0 "current=true"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; relc 1.0.0; publish; clone
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}" PATH="${t}/bin-fail:${t}/bin:${PATH}" FAIL_GIT_SUBCOMMAND=diff-tree
    check "tip: a failing git diff-tree is an error" 128 ""
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}" PATH="${t}/bin-fail:${t}/bin:${PATH}" FAIL_GIT_SUBCOMMAND=rev-list
    check "tip: a failing git rev-list is an error" 128 ""
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}" PATH="${t}/bin-fail:${t}/bin:${PATH}" FAIL_GIT_SUBCOMMAND=log
    check "tip: a failing git log is an error" 128 ""

    # production moves between the checkout and the step
    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; publish; clone
    cd "${t}/w"; commit "fix: later" a.txt; publish
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: production moved on with an ungated commit" 0 "current=false"
    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; publish; clone
    cd "${t}/w"; relc 1.0.0; publish
    run tip EVENT_NAME=workflow_run HEAD_SHA="${p}"; check "tip: production moved on with a release commit only" 0 "current=true"
    ensure "tip: the checkout was fast-forwarded to the tip" test "$(git -C "${t}/run" rev-parse HEAD)" = "$(git -C "${t}/run" rev-parse origin/production)"

    # tip, from a dispatch without a tag
    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; relc 1.0.0; publish; clone; gatefix "${p}"
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: newest non-release commit has a green gate" 0 "current=true"
    ensure "dispatch: gh was asked about that commit" grep -q "commits/${p}/check-runs" "${GH_LOG}"
    ensure "dispatch: the query names the check" grep -q "check_name=gate" "${GH_LOG}"
    ensure "dispatch: the query asks for a full page" grep -q "per_page=100" "${GH_LOG}"
    ensure "dispatch: a newer failing check of another name does not decide" test -n "$(jq -r '.check_runs[] | select(.name == "build") | .conclusion' "${t}/fix/default.json")"
    run tip EVENT_NAME=workflow_dispatch GH_MODE=fail; check "dispatch: gh fails" 1 "" "HTTP 502"
    for bad in garbage object; do
        run tip EVENT_NAME=workflow_dispatch GH_MODE="${bad}"; check "dispatch: gh answers ${bad}" nz "" "error"
        ensure "dispatch: ${bad} is refused by jq, not by the gate decision" test "$(grep -c 'no successful gate' <<< "${log}")" = 0
    done
    gatefix "${p}" '.check_runs[0].conclusion="failure"'
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: gate failed" 1 "" "no successful gate"
    gatefix "${p}" '.check_runs[0].status="in_progress" | .check_runs[0].conclusion=null'
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: gate still running" 1 "" "no successful gate"
    gatefix "${p}" '.check_runs[0].app.slug="another-app"'
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: green gate from another app" 1 "" "no successful gate"
    gatefix "${p}" '.check_runs=[] | .total_count=0'
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: no gate at all" 1 "" "no successful gate"
    gatefix "${p}" '.check_runs=[(.check_runs[0] | .conclusion="failure" | .started_at="2099-01-01T00:00:00Z"), .check_runs[0]]'
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: older green gate, newer failing re-run (listed first)" 1 "" "no successful gate"
    gatefix "${p}" '.check_runs=[.check_runs[0], (.check_runs[0] | .conclusion="failure" | .started_at="2000-01-01T00:00:00Z")]'
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: newer green gate, older failing run (listed last)" 0 "current=true"

    mkrepo; commit "fix: one" a.txt; publish; clone
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: HEAD itself is gated and not a release commit" 0 "current=true"

    mkrepo; commit "fix: one" a.txt; for n in $(seq 1 99); do commit "chore(release): 1.0.${n} [skip ci]" CHANGELOG.md; done; publish; clone
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: the normal commit is the 100th, inside the window" 0 "current=true"

    mkrepo; commit "fix: one" a.txt; for n in $(seq 1 100); do commit "chore(release): 1.0.${n} [skip ci]" CHANGELOG.md; done; publish; clone
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: the normal commit is the 101st, outside the window" 1 "" "no commit that is not a release commit"
    run tip EVENT_NAME=workflow_dispatch HEAD_SHA="$(sha HEAD)"; check "dispatch: a HEAD_SHA in the environment is ignored" 1 "" "no commit that is not a release commit"

    mkrepo; commit "fix: one" a.txt; p="$(sha HEAD)"; commit "docs: rewrite the changelog by hand" CHANGELOG.md; p2="$(sha HEAD)"; publish; clone; gatefix "${p2}"
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: a CHANGELOG.md-only commit with another subject is not skipped" 0 "current=true"
    ensure "dispatch: and the gate asked about is that commit's" grep -q "commits/${p2}/check-runs" "${GH_LOG}"

    mkrepo; commit "fix: one" a.txt; for n in $(seq 1 101); do commit "chore(release): 1.0.${n} [skip ci]" CHANGELOG.md; done; publish; clone
    run tip EVENT_NAME=workflow_dispatch; check "dispatch: only release commits in the last 100" 1 "" "no commit that is not a release commit"

    # repair: a tag handed to a dispatch. Hand-made tags sit among the genuine ones.
    mkrepo; commit "fix: one" a.txt; relc 1.0.0
    commit "fix: two" a.txt; git tag v1.5.0
    relc 1.0.1; git tag -d v1.0.1 > /dev/null; git tag -a -m release v1.0.1
    commit "chore(release): 2.0.0 [skip ci]" src.go; git tag v2.0.0
    relc 1.1.0; git tag v3.0.0
    commit "feat: top" a.txt; git tag v99.0.0
    git checkout -q -b side v1.0.0; commit "fix: side" b.txt; git tag v4.0.0; git checkout -q production
    publish; clone; echo false > "${t}/rel/v1.0.1"
    run repair TAG=v1.0.0 GITHUB_OUTPUT=; check "repair: no GITHUB_OUTPUT" 1 "" "::error::GITHUB_OUTPUT is not set"
    ensure "repair: nothing created without GITHUB_OUTPUT" test "$(gh_calls 'release create')" = 0
    run repair TAG=v1.0.0 GITHUB_REPOSITORY=; check "repair: no GITHUB_REPOSITORY" 1 "" "::error::GITHUB_REPOSITORY is not set"
    ensure "repair: nothing created without GITHUB_REPOSITORY" test "$(gh_calls 'release create')" = 0
    run repair TAG=v1.0.1; check "repair: genuine annotated release commit, release exists" 0 "tag=v1.0.1"
    ensure "repair: nothing created over an existing release" test "$(gh_calls 'release create')" = 0
    run repair TAG=v1.0.0; check "repair: genuine release commit, no release yet" 0 "tag=v1.0.0"
    ensure "repair: v1.0.0 is not marked latest" grep -q -- "release create v1.0.0 .*--latest=false" "${GH_LOG}"
    ensure "repair: the release is created in the right repository, from the tag, with notes" grep -q -- "release create v1.0.0 --repo example/repo --verify-tag --generate-notes" "${GH_LOG}"
    run repair TAG=v1.0.0; ensure "repair: the release list is not truncated" grep -q -- "release list --repo example/repo --limit 1000 " "${GH_LOG}"
    run repair TAG=v1.1.0; check "repair: the newest genuine release tag" 0 "tag=v1.1.0"
    ensure "repair: v1.1.0 is marked latest, v99.0.0 being a stray" grep -q -- "release create v1.1.0 .*--latest=true" "${GH_LOG}"
    run repair TAG=v1.5.0; check "repair: tag made by hand on a plain commit" 1 "" "does not point at the release commit"
    run repair TAG=v99.0.0; check "repair: v99.0.0 on a plain commit" 1 "" "does not point at the release commit"
    run repair TAG=v2.0.0; check "repair: release-titled commit that touches code" 1 "" "does not point at the release commit"
    run repair TAG=v3.0.0; check "repair: genuine commit of another version" 1 "" "does not point at the release commit"
    run repair TAG=v4.0.0; check "repair: tag on a side branch" 1 "" "is not an ancestor"
    run repair TAG=v9.9.9; check "repair: tag that does not exist" 1 "" "does not exist"
    for bad in 'v1.0' '1.0.1' 'v1.0.1-rc.1' 'v1.0.1; echo pwned' "\$(id)" 'v1.0.1 ' '' $'v1.0.1\nv1.0.0' 'refs/tags/v1.0.1' 'V1.0.1'; do
        run repair "TAG=${bad}"; check "repair: malformed tag $(printf '%q' "${bad}")" 1 "" "not of the form"
        ensure "repair: gh never called for that tag" test "$(grep -c . "${GH_LOG}")" = 0
    done
    rm -f "${t}/rel/v1.0.0"
    run repair TAG=v1.0.0 GH_MODE=fail; check "repair: gh fails" 1 "" "HTTP 502"
    ensure "repair: no release created after gh failed" test "$(gh_calls 'release create')" = 0
    for bad in garbage object; do
        run repair TAG=v1.0.0 GH_MODE="${bad}"; check "repair: gh answers ${bad}" nz "" "error"
        ensure "repair: no release created after gh answered ${bad}" test "$(gh_calls 'release create')" = 0
    done
    echo true > "${t}/rel/v1.1.0"
    run repair TAG=v1.1.0; check "repair: the release is a draft" 1 "" "is a draft"
    ensure "repair: no second release over a draft" test "$(gh_calls 'release create')" = 0

    # repair when production moved on after the checkout: the tag exists on the remote only
    mkrepo; commit "fix: one" a.txt; relc 1.0.0; publish; clone
    cd "${t}/w"; commit "fix: two" a.txt; relc 1.1.0; publish
    run repair TAG=v1.1.0; check "repair: production moved on, the tag is only on the remote" 0 "tag=v1.1.0"
    ensure "repair: and it is the newest release tag" grep -q -- "release create v1.1.0 .*--latest=true" "${GH_LOG}"

    # a tag that moved on the remote after the checkout replaces the stale one
    mkrepo; commit "fix: one" a.txt; git tag v1.0.0; publish; clone
    cd "${t}/w"; git tag -d v1.0.0 > /dev/null; relc 1.0.0; git push -q -f origin production --tags 2> /dev/null
    run repair TAG=v1.0.0; check "repair: the tag moved on the remote onto its release commit" 0 "tag=v1.0.0"

    # a tag deleted on the remote after the checkout no longer exists
    mkrepo; commit "fix: one" a.txt; relc 1.0.0; relc 1.0.1; publish; clone
    git -C "${t}/w" push -q origin :refs/tags/v1.0.1 2> /dev/null
    run repair TAG=v1.0.1; check "repair: the tag was deleted on the remote" 1 "" "does not exist"

    run bogus; check "usage: an unknown subcommand" 2 "" "usage:"
    run ""; check "usage: no subcommand" 2 "" "usage:"

    # newest: decided from the live tags of production
    mkrepo; commit "fix: one" a.txt; relc 1.9.0; commit "fix: two" a.txt; relc 1.10.0; commit "fix: three" a.txt; git tag v2.0.0-rc.1
    git tag v99.0.0                                                                       # stray tag on a plain commit, merged into production
    git checkout -q -b side; commit "fix: side" b.txt; relc 3.0.0; git push -q origin side --tags 2> /dev/null; git checkout -q production
    publish; clone; git -C "${t}/run" checkout -q v1.10.0
    run newest TAG=v1.10.0; check "newest: v1.10.0 beats v1.9.0, ignores a pre-release, a stray v99.0.0 and an unmerged v3.0.0" 0 "latest=true"
    git -C "${t}/run" checkout -q v1.9.0
    run newest TAG=v1.9.0; check "newest: an older release tag" 0 "latest=false"
    cd "${t}/w"; commit "fix: four" a.txt; relc 1.11.0; publish
    git -C "${t}/run" checkout -q v1.10.0
    run newest TAG=v1.10.0; check "newest: re-run after v1.11.0 landed (stale clone)" 0 "latest=false"
    git -C "${t}/run" checkout -q v1.11.0
    run newest TAG=v1.11.0; check "newest: v1.11.0" 0 "latest=true"

    # a tag moved on the remote after the checkout replaces the stale one
    mkrepo; commit "fix: one" a.txt; git tag v1.0.0; publish; clone
    cd "${t}/w"; git tag -d v1.0.0 > /dev/null; relc 1.0.0; git push -q -f origin production --tags 2> /dev/null
    run newest TAG=v1.0.0; check "newest: the tag moved on the remote onto its release commit" 0 "latest=true"

    # no release tag merged into production at all: the tag being built is not the newest
    mkrepo; commit "fix: one" a.txt; git checkout -q -b side; commit "fix: side" b.txt; relc 3.0.0
    git push -q origin side --tags 2> /dev/null; git checkout -q production; publish; clone; git -C "${t}/run" checkout -q v3.0.0
    run newest TAG=v3.0.0; check "newest: no release tag merged into production" 0 "latest=false"
    run newest TAG=; check "newest: an empty TAG is refused, not answered latest=true" 1 "" "::error::TAG is not set"
    run newest; check "newest: no TAG is refused" 1 "" "::error::TAG is not set"

    if [ "${n_fail}" -ne 0 ]; then
        echo "release-guard: self-test FAILED: ${n_fail} failed, ${n_ok} passed" >&2
        exit 1
    fi
    echo "release-guard: self-test ok: ${n_ok} assertions"
}

case "${1:-}" in
    tip) cmd_tip ;;
    repair) cmd_repair ;;
    newest) cmd_newest ;;
    --self-test) self_test ;;
    *)
        echo "usage: $0 tip|repair|newest|--self-test" >&2
        exit 2
        ;;
esac
