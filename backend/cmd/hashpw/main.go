// Command hashpw generates the bcrypt hash for the ADMIN_PASSWORD_HASH
// config key of the admin panel.
//
// Usage:
//
//	go run ./cmd/hashpw -p 'your-password'
//	go run ./cmd/hashpw          # prompts on stdin (hidden input not guaranteed)
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	pw := flag.String("p", "", "明文密码（省略则从 stdin 读取一行）")
	flag.Parse()

	password := *pw
	if password == "" {
		fmt.Print("输入密码: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		password = strings.TrimRight(line, "\r\n")
	}
	if len(password) < 6 {
		fmt.Fprintln(os.Stderr, "密码至少 6 位")
		os.Exit(1)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hash 失败:", err)
		os.Exit(1)
	}
	fmt.Println(string(h))
}
