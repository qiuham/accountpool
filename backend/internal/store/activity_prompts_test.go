package store

import "testing"

func TestRandomPrompts(t *testing.T) {
	for _, p := range [][]string{nil, {}, {""}, {"   "}, make([]string, 101)} {
		if _, err := RandomPrompt(p); err == nil {
			t.Fatal("invalid list accepted")
		}
	}
	choices := []string{"hello", "分享一个小知识", "你好"}
	for i := 0; i < 100; i++ {
		p, err := RandomPrompt(choices)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, v := range choices {
			found = found || p == v
		}
		if !found {
			t.Fatal("choice not in list")
		}
	}
}
