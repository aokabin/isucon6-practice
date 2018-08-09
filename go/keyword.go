package main

import (
	"strings"
	"sync"
	"fmt"
	"html"
	"crypto/sha1"
	"time"
)

var (
	keywordsMap map[int][]string
	lengthList []int // [40, 0, 0, ... 12]みたいなリストで、0番目には今最大の文字数が入って、それ以外はその番目がその要素の数

	hashReplacer *strings.Replacer
	linkReplacer *strings.Replacer

	replacerSync sync.RWMutex
	keywordSync sync.RWMutex

	replacerUpdated time.Time
)

type Keyword struct {
	Keyword string
	Length int
}

func setKeywords() []string {
	keywords := make([]string, 0)

	for i := lengthList[0]; i > 0; i-- {
		if v, ok := keywordsMap[i]; ok {
			keywords = append(keywords, v...)
		}
	}

	return keywords
}

func createKeywords() {
	rows, err := db.Query(`
		SELECT keyword, CHAR_LENGTH(keyword) as len FROM entry ORDER BY CHARACTER_LENGTH(keyword) DESC;
	`)
	panicIf(err)
	keywords := make([]*Keyword, 0)
	for rows.Next() {
		k := Keyword{}
		err := rows.Scan(&k.Keyword, &k.Length)
		panicIf(err)
		keywords = append(keywords, &k)
	}
	rows.Close()
	keywordsMap = make(map[int][]string, len(keywords)*2)
	lengthList = make([]int, 200, 200) //200文字以上はないという想定
	lengthList[0] = keywords[0].Length

	keywordSync.Lock()
	for _, k := range keywords {
		keywordsMap[k.Length] = append(keywordsMap[k.Length], k.Keyword)
		lengthList[k.Length]++
	}
	keywordSync.Unlock()
}

func updateReplacer() {

	hashStrings := make([]string, 0, 20000)
	linkStrings := make([]string, 0, 20000)

	keywordSync.RLock()
	for i := lengthList[0]; i > 0; i-- {
		if kws, ok := keywordsMap[i]; ok {
			for _, kw := range kws {
				hash := "isuda_" + fmt.Sprintf("%x", sha1.Sum([]byte(kw)))
				uri := "/keyword/" + pathURIEscape(kw)
				link := fmt.Sprintf("<a href=\"%s\">%s</a>", uri, html.EscapeString(kw))
				hashStrings = append(hashStrings, kw, hash)
				linkStrings = append(linkStrings, hash, link)
			}
		}
	}
	keywordSync.RUnlock()

	r1 := strings.NewReplacer(hashStrings...)
	r2 := strings.NewReplacer(linkStrings...)

	replacerSync.Lock()
	hashReplacer = r1
	linkReplacer = r2
	replacerSync.Unlock()
}

func AddKeyword(key string) {
	keywordLength := len(key)
	if _, ok := keywordsMap[keywordLength]; !ok {
		keywordsMap[keywordLength] = make([]string, 0)
	}
	keywordSync.Lock()
	keywordsMap[keywordLength] = append(keywordsMap[keywordLength], key)
	keywordSync.Unlock()
	lengthList[keywordLength]++

	updateReplacer()
}

func DeleteKeyword(key string) {
	kLen := len(key)
	for i, v := range keywordsMap[kLen] {
		if v == key{
			keywordsMap[kLen] = append(keywordsMap[kLen][:i], keywordsMap[kLen][i+1:]...)
		}
	}
	lengthList[kLen]--
	if lengthList[kLen] == 0 && lengthList[0] == kLen {
		for i := lengthList[0]; i > 0; i-- {
			if lengthList[i] > 1 {
				lengthList[0] = i
				break
			}
		}
	}
	updateReplacer()
}
