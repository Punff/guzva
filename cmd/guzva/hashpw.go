package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"guzva/internal/auth"
)

// hashpwMain bcrypt-hashes a password for GUZVA_ADMIN_HASH (§6.5). The
// password is read from stdin when not given as a flag, so interactive use
// never lands in shell history:
//
//	guzva hashpw            # type the password, Ctrl-D
//	echo 'secret' | guzva hashpw
func hashpwMain(args []string) int {
	fs := flag.NewFlagSet("hashpw", flag.ExitOnError)
	pw := fs.String("password", "", "password (prefer stdin to keep it out of shell history)")
	fs.Parse(args)

	p := *pw
	if p == "" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
		if err != nil {
			fmt.Fprintln(os.Stderr, "hashpw:", err)
			return 1
		}
		p = strings.TrimRight(string(b), "\r\n")
	}
	if p == "" {
		fmt.Fprintln(os.Stderr, "usage: guzva hashpw [-password pw]  (or pipe it on stdin)")
		return 2
	}
	h, err := auth.HashPassword(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hashpw:", err)
		return 1
	}
	fmt.Println(h)
	return 0
}
