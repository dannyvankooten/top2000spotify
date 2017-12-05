package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"github.com/xrash/smetrics"
	"github.com/zmb3/spotify"
)

const redirectURI = "http://localhost:8080/callback"

var (
	auth  = spotify.NewAuthenticator(redirectURI, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistModifyPublic)
	store = sessions.NewCookieStore([]byte("map[interface{}]interface{}"))
)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	auth.SetAuthInfo(os.Getenv("SPOTIFY_ID"), os.Getenv("SPOTIFY_SECRET"))

	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/callback", handleAuth)
	http.HandleFunc("/api/me", handlePing)
	http.HandleFunc("/api/test-list", handleTestList)
	http.HandleFunc("/api/create-playlist", handleCreatePlaylist)
	http.Handle("/", http.FileServer(http.Dir("./web")))
	http.ListenAndServe(":8080", nil)
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, "t2s")

	w.Header().Set("Content-Type", "application/json")

	if _, ok := sess.Values["token"]; ok && !sess.IsNew && sess.Values["token"].(string) != "" {
		w.Write([]byte("true"))
	} else {
		w.Write([]byte("false"))
	}
}

func handleTestList(w http.ResponseWriter, r *http.Request) {
	doc, err := goquery.NewDocument(r.FormValue("url"))
	if err != nil {
		// not even a webpage
		log.Fatal(err)
	}

	if doc.Find(".socials-like h1").Length() == 0 || doc.Find(".yourlist li").Length() == 0 {
		// not a playlist
		// TODO: Return error
	}
}

func handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {

	var data struct {
		URL string `json:"url"`
	}
	err := json.NewDecoder(r.Body).Decode(&data)

	sess, _ := store.Get(r, "t2s")
	if data.URL == "" {
		log.Fatal("Did not supply a proper URL")
	}

	doc, err := goquery.NewDocument(data.URL)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: Validate playlist (again?)

	token, err := auth.Exchange(sess.Values["token"].(string))
	if err != nil {
		log.Fatal(err)
	}
	client := auth.NewClient(token)
	user, err := client.CurrentUser()
	if err != nil {
		log.Fatal(err)
	}

	// create new playlist
	name := doc.Find(".socials-like h1").Text()
	name = strings.TrimPrefix(name, "De NPO Radio 2 ") + " (2017)"
	playlist, err := client.CreatePlaylistForUser(user.ID, name, true)
	if err != nil {
		log.Fatal(err)
	}

	// find all track id's
	tracks := make([]spotify.ID, 0)

	doc.Find(".yourlist li").Each(func(i int, s *goquery.Selection) {
		// For each item found, get the band and title
		artist := s.Find("h2").Text()
		title := s.Find("h3").Text()

		result, err := client.Search(artist+" "+title, spotify.SearchTypeTrack)
		if err != nil {
			log.Fatal(err)
		}

		for _, track := range result.Tracks.Tracks {
			if strings.HasPrefix(track.Name, title) || smetrics.WagnerFischer(title, track.Name, 1, 1, 2) < 5 {
				fmt.Printf("%s - %s\n", artist, title)
				fmt.Printf(track.Artists[0].Name + " - " + track.Name + "\n")

				tracks = append(tracks, track.ID)
				break
			}
		}
	})

	client.AddTracksToPlaylist(user.ID, playlist.ID, tracks...)

}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, "t2s")
	url := auth.AuthURL(sess.ID)
	http.Redirect(w, r, url, 301)
}

func handleAuth(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, "t2s")

	values := r.URL.Query()
	if e := values.Get("error"); e != "" {
		log.Fatal("spotify: auth failed - " + e)
	}

	// get code
	code := values.Get("code")
	if code == "" {
		http.NotFound(w, r)
		return
	}

	// check state
	if st := r.FormValue("state"); st != sess.ID {
		http.NotFound(w, r)
		return
	}

	// save token
	sess.Values["token"] = code
	err := sess.Save(r, w)
	if err != nil {
		log.Fatal(err)
	}

	// redirect back to home
	http.Redirect(w, r, "/", 301)
}
