package main

import (
	"encoding/json"
	"fmt"
	"github.com/ham/accountpool/backend/internal/store"
	"io"
	"os"
)

func cmdAdminReset(st *store.Store) {
	if len(os.Args) != 2 {
		die("admin-reset 从标准输入读取 JSON，不接受命令行密码")
	}
	var input struct {
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		die("请输入包含 password 的 JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		die("只允许一个 JSON 对象")
	}
	if err := st.ResetConsolePassword(input.Password); err != nil {
		die("%v", err)
	}
	fmt.Println("管理员密码已更新，全部管理台会话已撤销")
}
