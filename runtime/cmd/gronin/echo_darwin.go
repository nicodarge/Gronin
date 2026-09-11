package main

import "golang.org/x/sys/unix"

func disableEcho(fd int) error {
	termios, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return err
	}
	termios.Lflag &^= unix.ECHO
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, termios)
}
