package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/sessions"
	_ "github.com/joho/godotenv/autoload"
	"github.com/xrash/smetrics"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2"
)

const (
	sessionName    = "t2s"
	errInvalidList = "Dat lijstje lijkt nergens op. Of dat lijkt nergens op 'n lijstje."
	errSpotifyConn = "Je Spotify account werkt niet echt mee."
	errSpotifyAuth = "Zonder toestemming kan ik de playlist niet voor je maken."
	errInternal    = "Er gaat iets mis en het is mijn schuld. :("
)

var (
	redirectURI = os.Getenv("APP_URL") + "/callback"
	auth        = spotify.NewAuthenticator(redirectURI, spotify.ScopeUserReadPrivate, spotify.ScopePlaylistModifyPublic)
	store       = sessions.NewCookieStore([]byte("map[interface{}]interface{}"))
)

func main() {
	f, err := os.OpenFile("errors.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	log.SetOutput(f)

	store.Options.MaxAge = 3200 // little less than 1 hour
	auth.SetAuthInfo(os.Getenv("SPOTIFY_ID"), os.Getenv("SPOTIFY_SECRET"))

	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/logout", handleLogout)
	http.HandleFunc("/callback", handleAuth)
	http.HandleFunc("/api/me", handlePing)
	http.HandleFunc("/api/create-playlist", handleCreatePlaylist)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web"))))
	http.HandleFunc("/", handleHome)
	http.ListenAndServe(":9005", nil)
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "web/index.html")
}

func getAuthenticatedClient(r *http.Request) (spotify.Client, error) {
	sess, _ := store.Get(r, sessionName)
	if v, ok := sess.Values["accessToken"]; !ok || sess.IsNew || v.(string) == "" {
		return spotify.Client{}, errors.New("session is not authenticated with spotify")
	}

	token := &oauth2.Token{
		AccessToken: sess.Values["accessToken"].(string),
	}
	client := auth.NewClient(token)
	return client, nil
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// get current user
	client, err := getAuthenticatedClient(r)
	if err != nil {
		w.Write([]byte("false"))
		return
	}
	user, err := client.CurrentUser()
	if err != nil {
		w.Write([]byte("false"))
		return
	}

	imageURL := ""
	if len(user.Images) > 0 {
		imageURL = user.Images[0].URL
	}

	json.NewEncoder(w).Encode(map[string]string{
		"name":  user.ID,
		"image": imageURL,
	})
}

func handleCreatePlaylist(w http.ResponseWriter, r *http.Request) {

	var data struct {
		URL string `json:"url"`
	}
	err := json.NewDecoder(r.Body).Decode(&data)

	w.Header().Set("Content-Type", "application/json")
	je := json.NewEncoder(w)

	if err != nil || data.URL == "" {
		je.Encode(map[string]interface{}{
			"error": errInvalidList,
		})
		return
	}

	doc, err := goquery.NewDocument(data.URL)
	if err != nil {
		je.Encode(map[string]interface{}{
			"error": errInvalidList,
		})
		return
	}

	// Validate playlist
	heading := doc.Find(".socials-like h1")
	listItems := doc.Find(".yourlist li")

	if heading.Length() == 0 || listItems.Length() == 0 {
		je.Encode(map[string]interface{}{
			"error": errInvalidList,
		})
		return
	}

	// write lijstje URL to file so we can do stuff later
	f, err := os.OpenFile("lijstjes.dat", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err == nil {
		f.WriteString(time.Now().Format("2006-01-02 15:04:05") + " " + data.URL + "\n")
	}
	f.Close()

	// get client
	client, err := getAuthenticatedClient(r)
	if err != nil {
		je.Encode(map[string]interface{}{
			"error": errSpotifyConn,
		})
		return
	}
	user, err := client.CurrentUser()
	if err != nil {
		log.Println(err)
		je.Encode(map[string]interface{}{
			"error": errSpotifyConn,
		})
		return
	}

	// create new playlist
	name := heading.Text()
	name = strings.TrimPrefix(name, "De NPO Radio 2 ") + " (2017)"
	playlist, err := client.CreatePlaylistForUser(user.ID, name, true)
	if err != nil {
		log.Println(err)
		je.Encode(map[string]interface{}{
			"error": errSpotifyConn,
		})
		return
	}

	// find all track id's
	tracks := make([]spotify.ID, 0)
	listItems.Each(func(i int, s *goquery.Selection) {
		artist := s.Find("h2").Text()
		title := s.Find("h3").Text()

		// lowercase track title
		ID := searchForTrackID(client, artist, title, artist+" "+title)
		if ID == "" {
			ID = searchForTrackID(client, artist, title, artist+" "+title[0:(len(title)/2)])
		}

		if ID != "" {
			tracks = append(tracks, ID)
		} else {
			log.Printf("failed matching %s %s\n", artist, title)
		}
	})

	_, err = client.AddTracksToPlaylist(user.ID, playlist.ID, tracks...)
	if err != nil {
		log.Println(err)
		je.Encode(map[string]interface{}{
			"error": errSpotifyConn,
		})
		return
	}

	je.Encode(map[string]string{
		"playlist": playlist.ID.String(),
	})
}

func searchForTrackID(client spotify.Client, artist string, title string, q string) spotify.ID {
	result, err := client.Search(q, spotify.SearchTypeTrack)
	if err != nil {
		log.Println(err)
		return spotify.ID("")
	}

	title = strings.ToLower(title)
	artist = strings.ToLower(artist)

	for _, t := range result.Tracks.Tracks {
		trackMatched := false
		artistMatched := false

		// NORMALIZE TRACK
		t.Name = strings.ToLower(t.Name)
		t.Name = strings.TrimSuffix(t.Name, "remastered")
		t.Name = strings.TrimSpace(t.Name)
		t.Name = strings.TrimSuffix(t.Name, "-")
		t.Name = strings.TrimSpace(t.Name)

		// compare track name
		if smetrics.WagnerFischer(title, t.Name, 1, 1, 2) <= 5 {
			trackMatched = true
		}

		// compare each track artist
		for _, a := range t.Artists {
			a.Name = strings.ToLower(a.Name)
			if smetrics.WagnerFischer(artist, a.Name, 1, 1, 2) <= 5 {
				artistMatched = true
				break
			}
		}

		if trackMatched && artistMatched {
			return t.ID
		}
	}

	return spotify.ID("")
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, sessionName)
	url := auth.AuthURL(sess.ID)
	http.Redirect(w, r, url, 302)
}

func handleLogout(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, sessionName)
	sess.Options.MaxAge = -1
	sess.Values["accessToken"] = ""
	err := sess.Save(r, w)
	if err != nil {
		log.Println(err)
		http.Error(w, errInternal, http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", 302)
}

func handleAuth(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, sessionName)

	token, err := auth.Token(sess.ID, r)
	if err != nil {
		log.Println(err)
		http.Error(w, errSpotifyAuth, http.StatusUnauthorized)
		return
	}

	// save token
	//sess.Values["name"] = user.Name
	sess.Values["accessToken"] = token.AccessToken
	err = sess.Save(r, w)
	if err != nil {
		log.Println(err)
		http.Error(w, errInternal, http.StatusInternalServerError)
		return
	}

	// redirect back to home
	http.Redirect(w, r, "/", 302)
}
