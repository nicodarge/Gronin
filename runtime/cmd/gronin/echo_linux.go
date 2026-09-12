package main

import "golang.org/x/sys/unix"

// The request numbers are what differ between systems; the code around them does not.
const (
	ioctlGetTermios = unix.TCGETS
	ioctlSetTermios = unix.TCSETS
)
