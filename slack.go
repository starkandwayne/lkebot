package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"golang.org/x/net/websocket"
)

type Message struct {
	ID      uint64 `json:"id"`
	Type    string `json:"type"`
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

func (m Message) IsDirected(at string) bool {
	return strings.HasPrefix(m.Channel, "D") || strings.HasPrefix(m.Text, "<@"+at+">")
}

func getMessage(ws *websocket.Conn) (m Message, err error) {
	err = websocket.JSON.Receive(ws, &m)
	return
}

var counter uint64

func Send(ws *websocket.Conn, m Message) error {
	m.ID = atomic.AddUint64(&counter, 1)
	return websocket.JSON.Send(ws, m)
}

func Slack(token string) (*websocket.Conn, string, map[string]string) {
	url := fmt.Sprintf("https://slack.com/api/rtm.start?token=%s", token)
	res, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		err = fmt.Errorf("API request failed with code %d", res.StatusCode)
		log.Fatal(err)
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Fatal(err)
	}

	var r struct {
		Ok    bool   `json:"ok"`
		Error string `json:"error"`
		Url   string `json:"url"`
		Self  struct {
			ID string `json:"id"`
		} `json:"self"`

		Channels []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"channels"`
	}
	err = json.Unmarshal(body, &r)
	if err != nil {
		log.Fatal(err)
	}

	if !r.Ok {
		err = fmt.Errorf("Slack error: %s", r.Error)
		log.Fatal(err)
	}

	url = r.Url
	id := r.Self.ID

	chans := make(map[string]string)
	for _, c := range r.Channels {
		chans[c.Name] = c.ID
	}

	ws, err := websocket.Dial(url, "", "https://api.slack.com/")
	if err != nil {
		log.Fatal(err)
	}

	Send(ws, Message{
		Type:    "message",
		Channel: "#botspam",
		Text:    "lke in the house\n",
	})
	return ws, id, chans
}
