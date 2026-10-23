// Command seeddev seeds a dev database with one test service for
// local smoke testing. It is a developer tool, not part of the server.
package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: seeddev <db path>")
		os.Exit(1)
	}
	db, err := sql.Open("sqlite", os.Args[1])
	if err != nil {
		panic(err)
	}
	_, err = db.Exec(`INSERT INTO services (category_id,title,cover,price_cents,xpay_product_id,xpay_env,status,created_at,updated_at)
		VALUES (0,'测试服务','/uploads/test.png',100,'vip1',1,1,1,1)`)
	if err != nil {
		panic(err)
	}
	fmt.Println("seeded")
}
