// epicpanel-shell — restricted login shell for EpicPanel site users.
// Applied as the user's login shell via `usermod -s`. It blocks privileged
// commands and network servers while allowing normal file/site work.
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

var forbidden = []string{
	"sudo", "su ", "passwd", "useradd", "userdel", "usermod",
	"systemctl", "service ", "iptables", "mount", "umount",
	"apt", "apt-get", "dpkg", "reboot", "shutdown", "poweroff",
	"nc ", "ncat", "netcat", "socat", "tcpdump", "nmap",
	"crontab", "ssh ", "scp ", "rsync -e",
}

func main() {
	// Only the site's own user can run this shell (defense in depth).
	if os.Getuid() == 0 {
		fmt.Fprintln(os.Stderr, "epicpanel-shell: refusing to run as root")
		os.Exit(1)
	}
	if len(os.Args) >= 7 && os.Args[1] == "-c" {
		// sshd remote command execution: run the single command if allowed.
		cmdline := os.Args[6]
		if !allowed(cmdline) {
			fmt.Fprintf(os.Stderr, "epicpanel-shell: command not allowed: %s\n", firstWord(cmdline))
			os.Exit(126)
		}
		run(cmdline)
		return
	}
	// Interactive session.
	fmt.Println("EpicPanel restricted shell. Type 'exit' to quit. Allowed: site files, git, composer, wp, php, node.")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("$ ")
		if !scanner.Scan() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "logout" {
			return
		}
		if strings.HasPrefix(line, "cd ") {
			dir := strings.TrimSpace(strings.TrimPrefix(line, "cd "))
			if err := os.Chdir(dir); err != nil {
				fmt.Println(err)
			}
			continue
		}
		if !allowed(line) {
			fmt.Printf("epicpanel-shell: command not allowed: %s\n", firstWord(line))
			continue
		}
		run(line)
	}
}

func firstWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func allowed(cmdline string) bool {
	for _, bad := range forbidden {
		if strings.HasPrefix(cmdline, bad) || strings.Contains(cmdline, " "+bad) {
			return false
		}
	}
	// No path tricks to escape the check.
	if strings.Contains(cmdline, "..") && strings.Contains(cmdline, "/bin/") {
		return false
	}
	return true
}

func run(cmdline string) {
	cmd := exec.Command("/bin/sh", "-c", cmdline)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		if ws, ok := err.(*exec.ExitError); ok && ws.Sys().(syscall.WaitStatus).Signaled() {
			return
		}
		fmt.Fprintln(os.Stderr, err)
	}
}
