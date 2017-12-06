package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"github.com/xrash/smetrics"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2"
)

const redirectURI = "http://localhost:8080/callback"
const sessionName = "t2s"

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
	http.HandleFunc("/api/create-playlist", handleCreatePlaylist)
	http.Handle("/", http.FileServer(http.Dir("./web")))
	http.ListenAndServe(":8080", nil)
}

func getAuthenticatedClient(r *http.Request) spotify.Client {
	sess, _ := store.Get(r, sessionName)
	token := &oauth2.Token{
		AccessToken: sess.Values["accessToken"].(string),
	}
	client := auth.NewClient(token)
	return client
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, sessionName)

	w.Header().Set("Content-Type", "application/json")
	if _, ok := sess.Values["accessToken"]; !ok || sess.Values["accessToken"].(string) == "" {
		w.Write([]byte("false"))
		return
	}

	// get current user
	client := getAuthenticatedClient(r)
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
	w.Header().Set("Content-Type", "application/json")

	var data struct {
		URL string `json:"url"`
	}
	err := json.NewDecoder(r.Body).Decode(&data)
	if data.URL == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Dat lijstje lijkt nergens op! (Of dat lijkt nergens op 'n lijstje, sorry..)",
		})
		return
	}

	doc, err := goquery.NewDocument(data.URL)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Dat lijstje lijkt nergens op! (Of dat lijkt nergens op 'n lijstje, sorry..)",
		})
		return
	}

	// TODO: Validate playlist (again?)
	heading := doc.Find(".socials-like h1")
	listItems := doc.Find(".yourlist li")

	if heading.Length() == 0 || listItems.Length() == 0 {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Dat lijstje lijkt nergens op! (Of dat lijkt nergens op 'n lijstje, sorry..)",
		})
		return
	}

	// get client
	client := getAuthenticatedClient(r)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Je Spotify account wil niet echt meewerken...",
		})
		return
	}
	user, err := client.CurrentUser()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Je Spotify account wil niet echt meewerken...",
		})
		return
	}

	// create new playlist
	name := heading.Text()
	name = strings.TrimPrefix(name, "De NPO Radio 2 ") + " (2017)"
	playlist, err := client.CreatePlaylistForUser(user.ID, name, true)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Het lukt niet echt om de playlist te maken.",
		})
		return
	}

	// find all track id's
	tracks := make([]spotify.ID, 0)
	listItems.Each(func(i int, s *goquery.Selection) {
		artist := s.Find("h2").Text()
		title := s.Find("h3").Text()

		result, err := client.Search(artist+" "+title, spotify.SearchTypeTrack)
		if err != nil {
			return // rate limited?
		}

		// lowercase track title
		title = strings.ToLower(title)
		for _, track := range result.Tracks.Tracks {
			track.Name = strings.ToLower(track.Name)
			if (strings.HasPrefix(track.Name, title) && !strings.Contains(track.Name, "instrumental")) || smetrics.WagnerFischer(title, track.Name, 1, 1, 2) < 5 {
				tracks = append(tracks, track.ID)
				break
			}
		}
	})

	client.AddTracksToPlaylist(user.ID, playlist.ID, tracks...)

	json.NewEncoder(w).Encode(map[string]string{
		"playlist": playlist.ID.String(),
	})
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, sessionName)
	url := auth.AuthURL(sess.ID)
	http.Redirect(w, r, url, 301)
}

func handleAuth(w http.ResponseWriter, r *http.Request) {
	sess, _ := store.Get(r, sessionName)

	token, err := auth.Token(sess.ID, r)
	if err != nil {
		http.Error(w, "Ik heb toestemming nodig", http.StatusUnauthorized)
		w.Write([]byte("Ik heb wel toestemming nodig om de playlist in je Spotify account te maken..."))
		return
	}

	// save token
	//sess.Values["name"] = user.Name
	sess.Values["accessToken"] = token.AccessToken
	err = sess.Save(r, w)
	if err != nil {
		http.Error(w, "Er gaat iets gruwelijk mis en het is mijn schuld.", http.StatusInternalServerError)
		return
	}

	// redirect back to home
	http.Redirect(w, r, "/", 301)
}
