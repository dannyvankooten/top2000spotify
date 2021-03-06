package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"regexp"

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

	re := regexp.MustCompile(`\/share\/(\w+)$`)
	matches := re.FindStringSubmatch(data.URL)
	if matches == nil || len(matches) < 1 {
		je.Encode(map[string]interface{}{
			"error": errInvalidList,
		})
		return
	}

	id := matches[1]
	resp, err := http.Get("https://stem-backend.npo.nl/api/form/top-2000/" + id)
	if err != nil {
		je.Encode(map[string]interface{}{
			"error": errInvalidList,
		})
		return
	}
	defer resp.Body.Close()

	var list struct {
		Name string `json:"name"`
		Items []struct {
			ID string `json:"_id"`
			Source struct {
				Artist string `json:"artist"`
				Title string `json:"title"`
				SpotifyImage string `json:"spotifyImage"`
			} `json:"_source"`
		} `json:"shortlist"`

	}
	err = json.NewDecoder(resp.Body).Decode(&list)
	if err != nil {
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
		log.Println(err)
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
	name := list.Name + "'s Top 2000 lijstje (2019)"
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
	for _, track := range list.Items {
		t := track.Source

		// lowercase track title
		ID := searchForTrackID(client, t.Artist, t.Title, t.Artist+" "+t.Title)
		if ID == "" {
			ID = searchForTrackID(client, t.Artist, t.Title, t.Artist+" "+t.Title[0:(len(t.Title)/2)])
		}

		if ID != "" {
			tracks = append(tracks, ID)
		} else {
			log.Printf("failed matching %s %s\n", t.Artist, t.Title)
		}
	}


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

	// WagnerFischer on all tracks
	for _, t := range result.Tracks.Tracks {
		trackMatched := false
		artistMatched := false

		// normalize track name
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

	// String prefix on all track names
	for _, t := range result.Tracks.Tracks {
		trackMatched := false
		artistMatched := false

		t.Name = strings.ToLower(t.Name)

		// search for track name prefix but skip instrumental versions
		if strings.HasPrefix(t.Name, title) && !strings.Contains(t.Name, "instrumental") {
			trackMatched = true
		}

		// wagnerfischer on artist names
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
