package run

// OnPoll sets what the file lock calls each time a waiter polls a playbook's lock, so that
// a test can act at the instant the poll happens.
func (l *FileLock) OnPoll(hook func(name string)) { l.poll = hook }
