package main

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Songmu/strrand"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/unrolled/render"
	_ "net/http/pprof"
)

const (
	sessionName   = "isuda_session"
	sessionSecret = "tonymoris"
)

var (
	isutarEndpoint string
	isupamEndpoint string

	baseUrl *url.URL
	db      *sql.DB
	re      *render.Render
	store   *sessions.CookieStore

	errInvalidUser = errors.New("Invalid User")

	keywordsMap map[int][]string
	lengthList []int // [40, 0, 0, ... 12]みたいなリストで、0番目には今最大の文字数が入って、それ以外はその番目がその要素の数
)

func setName(w http.ResponseWriter, r *http.Request) error {
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if !ok {
		return nil
	}
	setContext(r, "user_id", userID)
	row := db.QueryRow(`SELECT name FROM user WHERE id = ?`, userID)
	user := User{}
	err := row.Scan(&user.Name)
	if err != nil {
		if err == sql.ErrNoRows {
			return errInvalidUser
		}
		panicIf(err)
	}
	setContext(r, "user_name", user.Name)
	return nil
}

func authenticate(w http.ResponseWriter, r *http.Request) error {
	if u := getContext(r, "user_id"); u != nil {
		return nil
	}
	return errInvalidUser
}

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM entry WHERE id > 7101`)
	panicIf(err)

	resp, err := http.Get(fmt.Sprintf("%s/initialize", isutarEndpoint))
	panicIf(err)
	defer resp.Body.Close()

	keywordsMap, lengthList = createKeywords()

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func topHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	perPage := 10
	p := r.URL.Query().Get("page")
	if p == "" {
		p = "1"
	}
	page, _ := strconv.Atoi(p)

	// このrowsは全データ取ってきている、使うのかな？
	rows, err := db.Query(fmt.Sprintf(
		"SELECT * FROM entry ORDER BY updated_at DESC LIMIT %d OFFSET %d",
		perPage, perPage*(page-1),
	))
	if err != nil && err != sql.ErrNoRows {
		panicIf(err)
	}
	entries := make([]*Entry, 0, 10)
	keywords := setKeywords()
	for rows.Next() {
		e := Entry{}
		err := rows.Scan(&e.ID, &e.AuthorID, &e.Keyword, &e.Description, &e.UpdatedAt, &e.CreatedAt)
		panicIf(err)
		e.Html = htmlify(w, r, e.Description, keywords)
		e.Stars = loadStars(e.Keyword)
		entries = append(entries, &e)
	}
	rows.Close()

	var totalEntries int
	row := db.QueryRow(`SELECT COUNT(*) FROM entry`)
	err = row.Scan(&totalEntries)
	if err != nil && err != sql.ErrNoRows {
		panicIf(err)
	}

	lastPage := int(math.Ceil(float64(totalEntries) / float64(perPage)))
	pages := make([]int, 0, 10)
	start := int(math.Max(float64(1), float64(page-5)))
	end := int(math.Min(float64(lastPage), float64(page+5)))
	for i := start; i <= end; i++ {
		pages = append(pages, i)
	}

	topResponse := TopResponse{
		Context: r.Context(),
		Entries: entries,
		Page: page,
		LastPage: lastPage,
		Pages: pages,
	}

	re.HTML(w, http.StatusOK, "index", topResponse)
}

func robotsHandler(w http.ResponseWriter, r *http.Request) {
	notFound(w)
}

func keywordPostHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}
	if err := authenticate(w, r); err != nil {
		forbidden(w)
		return
	}

	keyword := r.FormValue("keyword")
	if keyword == "" {
		badRequest(w)
		return
	}
	userID := getContext(r, "user_id").(int)
	description := r.FormValue("description")

	if isSpamContents(description) || isSpamContents(keyword) {
		http.Error(w, "SPAM!", http.StatusBadRequest)
		return
	}
	_, err := db.Exec(`
		INSERT INTO entry (author_id, keyword, description, created_at, updated_at)
		VALUES (?, ?, ?, NOW(), NOW())
		ON DUPLICATE KEY UPDATE
		author_id = ?, keyword = ?, description = ?, updated_at = NOW()
	`, userID, keyword, description, userID, keyword, description)
	panicIf(err)

	keywordLength := len(keyword)
	if _, ok := keywordsMap[keywordLength]; !ok {
		keywordsMap[keywordLength] = make([]string, 0)
	}
	keywordsMap[keywordLength] = append(keywordsMap[keywordLength], keyword)
	lengthList[keywordLength]++

	http.Redirect(w, r, "/", http.StatusFound)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	re.HTML(w, http.StatusOK, "authenticate", struct {
		Context context.Context
		Action  string
	}{
		r.Context(), "login",
	})
}

func loginPostHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	row := db.QueryRow(`SELECT * FROM user WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID, &user.Name, &user.Salt, &user.Password, &user.CreatedAt)
	if err == sql.ErrNoRows || user.Password != fmt.Sprintf("%x", sha1.Sum([]byte(user.Salt+r.FormValue("password")))) {
		forbidden(w)
		return
	}
	panicIf(err)
	session := getSession(w, r)
	session.Values["user_id"] = user.ID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	re.HTML(w, http.StatusOK, "authenticate", struct {
		Context context.Context
		Action  string
	}{
		r.Context(), "register",
	})
}

func registerPostHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	pw := r.FormValue("password")
	if name == "" || pw == "" {
		badRequest(w)
		return
	}
	userID := register(name, pw)
	session := getSession(w, r)
	session.Values["user_id"] = userID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func register(user string, pass string) int64 {
	salt, err := strrand.RandomString(`....................`)
	panicIf(err)
	res, err := db.Exec(`INSERT INTO user (name, salt, password, created_at) VALUES (?, ?, ?, NOW())`,
		user, salt, fmt.Sprintf("%x", sha1.Sum([]byte(salt+pass))))
	panicIf(err)
	lastInsertID, _ := res.LastInsertId()
	return lastInsertID
}

// TODO: KEYWORDのところ
func keywordByKeywordHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	keyword, _ := url.QueryUnescape(mux.Vars(r)["keyword"])
	query := "SELECT * FROM entry WHERE keyword = ?"
	row := db.QueryRow(query, keyword)
	e := Entry{}
	err := row.Scan(&e.ID, &e.AuthorID, &e.Keyword, &e.Description, &e.UpdatedAt, &e.CreatedAt)
	if err == sql.ErrNoRows {
		notFound(w)
		return
	}
	keywords := setKeywords()
	e.Html = htmlify(w, r, e.Description, keywords)
	e.Stars = loadStars(e.Keyword)

	resp := GetKeyWordResponse{
		Context: r.Context(),
		Entry: e,
	}

	re.HTML(w, http.StatusOK, "keyword", resp)
}

func keywordByKeywordDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}
	if err := authenticate(w, r); err != nil {
		forbidden(w)
		return
	}

	keyword, _ := url.QueryUnescape(mux.Vars(r)["keyword"])
	if keyword == "" {
		badRequest(w)
		return
	}
	if r.FormValue("delete") == "" {
		badRequest(w)
		return
	}
	row := db.QueryRow(`SELECT * FROM entry WHERE keyword = ?`, keyword)
	e := Entry{}
	err := row.Scan(&e.ID, &e.AuthorID, &e.Keyword, &e.Description, &e.UpdatedAt, &e.CreatedAt)
	if err == sql.ErrNoRows {
		notFound(w)
		return
	}
	_, err = db.Exec(`DELETE FROM entry WHERE keyword = ?`, keyword)
	panicIf(err)

	kLen := len(keyword)
	for i, v := range keywordsMap[kLen] {
		if v == keyword {
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

	http.Redirect(w, r, "/", http.StatusFound)
}

// html化する関数かな
func htmlify(w http.ResponseWriter, r *http.Request, content string, keywords []string) string {
	if content == "" {
		return ""
	}
	// TODO:A3 string.Replacerを使ってみる

	includedKeywords := make([]string, 0, 1000)

	for _, kw := range keywords {
		if strings.Index(content, kw) != -1 {
			includedKeywords = append(includedKeywords, kw)
		}
	}

	hashStrings := make([]string, 0, len(includedKeywords))
	linkStrings := make([]string, 0, len(includedKeywords))

	for _, kw := range includedKeywords {
		hash := "isuda_" + fmt.Sprintf("%x", sha1.Sum([]byte(kw)))
		uri := baseUrl.String()+"/keyword/" + pathURIEscape(kw)
		link := fmt.Sprintf("<a href=\"%s\">%s</a>", uri, html.EscapeString(kw))
		hashStrings = append(hashStrings, kw, hash)
		linkStrings = append(linkStrings, hash, link)
	}

	hashReplacer := strings.NewReplacer(hashStrings...)
	linkReplacer := strings.NewReplacer(linkStrings...)

	content = hashReplacer.Replace(content)
	content = html.EscapeString(content)
	content = linkReplacer.Replace(content)

	return strings.Replace(content, "\n", "<br />\n", -1)
}

func loadStars(keyword string) []*Star {
	v := url.Values{}
	v.Set("keyword", keyword)
	resp, err := http.Get(fmt.Sprintf("%s/stars", isutarEndpoint) + "?" + v.Encode())
	panicIf(err)
	defer resp.Body.Close()

	var data struct {
		Result []*Star `json:result`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	panicIf(err)
	return data.Result
}

func isSpamContents(content string) bool {
	v := url.Values{}
	v.Set("content", content)
	resp, err := http.PostForm(isupamEndpoint, v)
	panicIf(err)
	defer resp.Body.Close()

	var data struct {
		Valid bool `json:valid`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	panicIf(err)
	return !data.Valid
}

func getContext(r *http.Request, key interface{}) interface{} {
	return r.Context().Value(key)
}

func setContext(r *http.Request, key, val interface{}) {
	if val == nil {
		return
	}

	r2 := r.WithContext(context.WithValue(r.Context(), key, val))
	*r = *r2
}

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)
	return session
}

func main() {
	go func() {
	        log.Println(http.ListenAndServe("localhost:6060", nil))
	}()
	host := os.Getenv("ISUDA_DB_HOST")
	if host == "" {
		host = "localhost"
	}
	portstr := os.Getenv("ISUDA_DB_PORT")
	if portstr == "" {
		portstr = "3306"
	}
	port, err := strconv.Atoi(portstr)
	if err != nil {
		log.Fatalf("Failed to read DB port number from an environment variable ISUDA_DB_PORT.\nError: %s", err.Error())
	}
	user := os.Getenv("ISUDA_DB_USER")
	if user == "" {
		user = "root"
	}
	password := os.Getenv("ISUDA_DB_PASSWORD")
	if password == "" {
		password = "root"
	}
	dbname := os.Getenv("ISUDA_DB_NAME")
	if dbname == "" {
		dbname = "isuda"
	}

	db, err = sql.Open("mysql", fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?loc=Local&parseTime=true",
		user, password, host, port, dbname,
	))
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}
	db.Exec("SET SESSION sql_mode='TRADITIONAL,NO_AUTO_VALUE_ON_ZERO,ONLY_FULL_GROUP_BY'")
	db.Exec("SET NAMES utf8mb4")

	isutarEndpoint = os.Getenv("ISUTAR_ORIGIN")
	if isutarEndpoint == "" {
		isutarEndpoint = "http://localhost:5001"
	}
	isupamEndpoint = os.Getenv("ISUPAM_ORIGIN")
	if isupamEndpoint == "" {
		isupamEndpoint = "http://localhost:5050"
	}

	store = sessions.NewCookieStore([]byte(sessionSecret))

	re = render.New(render.Options{
		Directory: "views",
		Funcs: []template.FuncMap{
			{
				"url_for": func(path string) string {
					return baseUrl.String() + path
				},
				"title": func(s string) string {
					return strings.Title(s)
				},
				"raw": func(text string) template.HTML {
					return template.HTML(text)
				},
				"add": func(a, b int) int { return a + b },
				"sub": func(a, b int) int { return a - b },
				"entry_with_ctx": func(entry Entry, ctx context.Context) *EntryWithCtx {
					return &EntryWithCtx{Context: ctx, Entry: entry}
				},
			},
		},
	})

	r := mux.NewRouter()
	r.UseEncodedPath()
	r.HandleFunc("/", myHandler(topHandler))
	r.HandleFunc("/initialize", myHandler(initializeHandler)).Methods("GET")
	r.HandleFunc("/robots.txt", myHandler(robotsHandler))
	r.HandleFunc("/keyword", myHandler(keywordPostHandler)).Methods("POST")

	l := r.PathPrefix("/login").Subrouter()
	l.Methods("GET").HandlerFunc(myHandler(loginHandler))
	l.Methods("POST").HandlerFunc(myHandler(loginPostHandler))
	r.HandleFunc("/logout", myHandler(logoutHandler))

	g := r.PathPrefix("/register").Subrouter()
	g.Methods("GET").HandlerFunc(myHandler(registerHandler))
	g.Methods("POST").HandlerFunc(myHandler(registerPostHandler))

	k := r.PathPrefix("/keyword/{keyword}").Subrouter()
	k.Methods("GET").HandlerFunc(myHandler(keywordByKeywordHandler))
	k.Methods("POST").HandlerFunc(myHandler(keywordByKeywordDeleteHandler))

	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./public/")))
	log.Fatal(http.ListenAndServe(":5000", r))
}



// NOTE: Original Methods

func setKeywords() []string {
	// ちょっと勘違いしてたけど、文字列が長いものから順に並べてるだけか
	//rows, err := db.Query(`
	//	SELECT keyword FROM entry ORDER BY CHARACTER_LENGTH(keyword) DESC
	//`)
	//panicIf(err)
	//entries := make([]*Entry, 0)
	//for rows.Next() {
	//	e := Entry{}
	//	err := rows.Scan(&e.Keyword)
	//	panicIf(err)
	//	entries = append(entries, &e)
	//}
	//rows.Close()
	//keywords := make([]string, 0, len(entries))
	//for _, entry := range entries {
	//	keywords = append(keywords, entry.Keyword)
	//}

	keywords := make([]string, 0)

	for i := lengthList[0]; i > 0; i-- {
		if v, ok := keywordsMap[i]; ok {
			keywords = append(keywords, v...)
		}
	}

	return keywords
}

func joinedKeyWords(keywords []string) string {
	return strings.Join(keywords, "|")
}

type Keyword struct {
	Keyword string
	Length int
}

func createKeywords() (map[int][]string, []int) {
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
	keywordsMap := make(map[int][]string, len(keywords)+1000)
	lengthList := make([]int, 200, 200) //200文字以上はないという想定
	lengthList[0] = keywords[0].Length

	for _, k := range keywords {
		keywordsMap[k.Length] = append(keywordsMap[k.Length], k.Keyword)
		lengthList[k.Length]++
	}

	return keywordsMap, lengthList
}
