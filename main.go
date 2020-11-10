package main

import (
	"fmt"
	"log"
	"regexp"
	"os"
	"strings"
	"time"

	"github.com/jhunt/go-cli"
	env "github.com/jhunt/go-envirotron"
)

func main() {
	var opts struct {
		SlackToken  string `cli:"--slack" env:"LKEBOT_SLACK_TOKEN"`
		LinodeToken string `cli:"--linode" env:"LKEBOT_LINODE_TOKEN"`

		SweepInterval int    `cli:"--sweep" env:"LKEBOT_SWEEP_INTERVAL"`
		Channel       string `cli:"--channel" env:"LKEBOT_CHANNEL"`

		MaxClusters int `cli:"--max-clusters" env:"LKEBOT_MAX_CLUSTERS"`
		MaxNodes    int `cli:"--max-nodes" env:"LKEBOT_MAX_NODES"`

		DefaultRegion   string `cli:"--default-region" env:"LKEBOT_DEFAULT_REGION"`
		DefaultInstance string `cli:"--default-instance" env:"LKEBOT_DEFAULT_INSTANCE"`
		DefaultSize     int    `cli:"--default-size" env:"LKEBOT_DEFAULT_SIZE"`
		DefaultVersion  string `cli:"--default-k8s-version" env:"LKEBOT_DEFAULT_K8S_VERSION"`

		Blacklist []string `cli:"--blacklist"`
	}
	opts.SweepInterval = 1
	opts.MaxClusters = 5
	opts.MaxNodes = 5
	opts.DefaultRegion = "us-east"
	opts.DefaultInstance = "g6-standard-2"
	opts.DefaultSize = 1
	opts.DefaultVersion = "1.18"
	env.Override(&opts)
	if len(opts.Blacklist) == 0 {
		if v := os.Getenv("LKEBOT_BLACKLIST_CLUSTERS"); v != "" {
			opts.Blacklist = strings.Fields(strings.ReplaceAll(v, ",", " "))
		}
	}
	for _, blacklisted := range opts.Blacklist {
		fmt.Printf("blacklisting [%s]\n", blacklisted)
	}

	cmd, args, err := cli.Parse(&opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oops: %s\n", err)
		os.Exit(1)
	}

	ok := true
	if cmd != "" || len(args) != 0 {
		fmt.Fprintf(os.Stderr, "oops: extra arguments given to lkebot command\n")
		ok = false
	}
	if opts.SlackToken == "" {
		fmt.Fprintf(os.Stderr, "oops: missing required --slack flag\n")
		ok = false
	}
	if opts.LinodeToken == "" {
		fmt.Fprintf(os.Stderr, "oops: missing required --linode flag\n")
		ok = false
	}
	if opts.DefaultRegion == "" {
		fmt.Fprintf(os.Stderr, "oops: missing required --default-region flag\n")
		ok = false
	}
	if opts.DefaultInstance == "" {
		fmt.Fprintf(os.Stderr, "oops: missing required --default-instance flag\n")
		ok = false
	}
	if opts.DefaultSize < 1 {
		fmt.Fprintf(os.Stderr, "oops: invalid --default-size value\n")
		ok = false
	}
	if !ok {
		os.Exit(1)
	}

	// connect to linode
	c, err := Connect(opts.LinodeToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "oops: %s\n", err)
		os.Exit(1)
	}
	c.Blacklist(opts.Blacklist...)

	// connect to slack
	ws, id, chans := Slack(opts.SlackToken)
	fmt.Println("lkebot ready, ^C exits")

	if opts.SweepInterval > 0 {
		fmt.Printf("sweeping expired clusters every %d minutes.\n", opts.SweepInterval)
		go func() {
			t := time.NewTicker(time.Duration(opts.SweepInterval) * time.Minute)
			for range t.C {
				fmt.Printf("sweeping Linode instances...\n")
				swept := c.Sweep()
				if ch, found := chans[opts.Channel]; found {
					for _, name := range swept {
						Send(ws, Message{
							Type:    "message",
							Channel: ch,
							Text:    fmt.Sprintf("*%s* expired, so i tore it down.\n", name),
						})
					}
				} else {
					for _, name := range swept {
						fmt.Printf("swept up %s\n", name)
					}
				}
			}
		}()
	} else {
		fmt.Printf("NOT sweeping expired clusters...\n")
	}

	for {
		m, err := getMessage(ws)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Printf("[%s in %s] %s\n", m.Type, m.Channel, m.Text)
		if m.Type != "message" || !m.IsDirected(id) {
			continue
		}

		var vocab struct {
			Help  struct{} `cli:"help"`
			Info  struct{} `cli:"info"`
			List  struct{} `cli:"list"`
			Renew struct {
				For int `cli:"--for"`
			} `cli:"renew"`
			Expire struct{} `cli:"expire"`
			Deploy struct {
				Version  string `cli:"-v, --version"`
				Region   string `cli:"-r, --region"`
				Instance string `cli:"-i, --using"`
				Size     int    `cli:"-n, --nodes"`
				For      int    `cli:"--for"`
			} `cli:"deploy"`
			Teardown struct{} `cli:"teardown"`
			Access   struct{} `cli:"access"`
		}
		vocab.Renew.For = 1
		vocab.Deploy.Version = "1.18"
		vocab.Deploy.Region = opts.DefaultRegion
		vocab.Deploy.Instance = opts.DefaultInstance
		vocab.Deploy.Size = opts.DefaultSize
		vocab.Deploy.For = 8

		// we lcase everything and remove the leading `@<...>` hailing protocol
		re := regexp.MustCompile(`^<@` + id + `>\s+`)
		fmt.Printf("mesg:[%s]\n", m.Text)
		cmd, args, err := cli.ParseArgs(&vocab, strings.Fields(strings.ToLower(string(re.ReplaceAllString(m.Text, "")))))
		if err != nil {
			m.Text = "i have no clue what you are talking about...\n"
			Send(ws, m)
			continue
		}

		switch cmd {
		case "help":
			m.Text = "hi there! i can help deploy Linode LKE instances.\n\n"
			m.Text += "say `info` to get my current limits / parameters.\n"
			m.Text += "say `list` to see currently deployed lab clusters.\n"
			m.Text += "say `deploy NAME` to deploy a new lab cluster.\n"
			m.Text += "say `renew NAME` to renew the lease on a cluster.\n"
			m.Text += "say `expire NAME` to drop the lease on a cluster.\n"
			m.Text += "say `teardown NAME` to decommission a lab cluster.\n"
			m.Text += "say `access NAME` to get a cluster's kubeconfig\n"
			Send(ws, m)

		case "info":
			m.Text = fmt.Sprintf("i'm allowed to deploy up to *%d clusters*,\n", opts.MaxClusters)
			m.Text += fmt.Sprintf("each of which can be (at most) *%d nodes* in size.\n", opts.MaxNodes)
			if len(opts.Blacklist) != 0 {
				m.Text += fmt.Sprintf("i'm forbidden from interacting with [%s]\n", strings.Join(opts.Blacklist, ", "))
			}
			if opts.SweepInterval > 0 {
				m.Text += fmt.Sprintf("i check for (and teardown!) expired clusters every *%d minutes*.\n", opts.SweepInterval)
			}
			Send(ws, m)

		case "list":
			go func(m Message) {
				clusters, err := c.List()
				if err != nil {
					m.Text = fmt.Sprintf("oops: %s\n", err)

				} else {
					m.Text = fmt.Sprintf("found %d cluster(s):\n", len(clusters))
					for _, cluster := range clusters {
						m.Text += fmt.Sprintf("%s\n", cluster)
					}
				}
				Send(ws, m)
			}(m)

		case "renew":
			go func(m Message) {
				if len(args) != 1 {
					m.Text = "hrmm.  try `renew NAME-OF-CLUSTER` instead...\n"

				} else if vocab.Renew.For < 1 || vocab.Renew.For > 48 {
					m.Text = fmt.Sprintf("uh-oh; i'm afraid i can't let you renew a cluster for %d hours...\n", vocab.Renew.For)

				} else {
					cluster, err := c.Find(args[0])
					if err != nil {
						m.Text = fmt.Sprintf("oops: %s\n", err)

					} else if cluster == nil {
						m.Text = fmt.Sprintf("i was not able to find the cluster *%s*\n", args[0])

					} else {
						cluster.Renew(time.Duration(vocab.Renew.For) * time.Hour)
						m.Text = fmt.Sprintf("ok.  i renewed *%s* for %d more hour(s)\n", cluster.Name, vocab.Renew.For)
					}
				}
				Send(ws, m)
			}(m)

		case "expire":
			go func(m Message) {
				if len(args) != 1 {
					m.Text = "hrmm.  try `expire NAME-OF-CLUSTER` instead...\n"

				} else {
					cluster, err := c.Find(args[0])
					if err != nil {
						m.Text = fmt.Sprintf("oops: %s\n", err)

					} else if cluster == nil {
						m.Text = fmt.Sprintf("i was not able to find the cluster *%s*\n", args[0])

					} else {
						cluster.Expire()
						m.Text = fmt.Sprintf("ok.  i expired *%s*\n", cluster.Name)
					}
				}
				Send(ws, m)
			}(m)

		case "deploy": // deploy NAME [in REGION] [using TYPE] [nodes SIZE]
			go func(m Message) {
				if len(args) != 1 {
					m.Text = "hrmm.  try `deploy NAME-OF-CLUSTER` instead...\n"

				} else if c.Count() >= opts.MaxClusters {
					m.Text = fmt.Sprintf("oof! unfortunately we're fresh out of space for new clusters...\n")

				} else if vocab.Deploy.For < 1 || vocab.Deploy.For > 48 {
					m.Text = fmt.Sprintf("uh-oh; i'm afraid i can't let you deploy a cluster for %d hours...\n", vocab.Deploy.For)

				} else if vocab.Deploy.Size < 1 || vocab.Deploy.Size > opts.MaxNodes {
					m.Text = fmt.Sprintf("uh-oh; i'm afraid i can't let you deploy a %d-node cluster...\n", vocab.Deploy.Size)

				} else {
					m.Text = "hang on, deploying...\n"
					Send(ws, m)

					cluster, err := c.Deploy(Deployment{
						Name:     args[0],
						Version:  vocab.Deploy.Version,
						Region:   vocab.Deploy.Region,
						Instance: vocab.Deploy.Instance,
						Size:     vocab.Deploy.Size,
						Lifetime: time.Duration(vocab.Deploy.For) * time.Hour,
					})
					if err != nil {
						m.Text = fmt.Sprintf("oops: %s\n", err)

					} else {
						m.Text = fmt.Sprintf("alright.  cluster *%s* is deploying.\n", cluster.Name)
					}
				}
				Send(ws, m)
			}(m)

		case "teardown":
			go func(m Message) {
				if len(args) != 1 {
					m.Text = "hrmm.  try `teardown NAME-OF-CLUSTER` instead...\n"

				} else {
					cluster, err := c.Find(args[0])
					if err != nil {
						m.Text = fmt.Sprintf("oops: %s\n", err)

					} else if cluster == nil {
						m.Text = fmt.Sprintf("i was not able to find the cluster *%s*\n", args[0])

					} else {
						c.Teardown(cluster)
						m.Text = fmt.Sprintf("ok.  tearing down *%s*\n", cluster.Name)
					}
				}
				Send(ws, m)
			}(m)

		case "access":
			go func(m Message) {
				if len(args) != 1 {
					m.Text = "hrmm.  try `access NAME-OF-CLUSTER` instead...\n"

				} else {
					cluster, err := c.Find(args[0])
					if err != nil {
						m.Text = fmt.Sprintf("oops: %s\n", err)

					} else if cluster == nil {
						m.Text = fmt.Sprintf("i was not able to find the cluster *%s*\n", args[0])

					} else {
						kc, err := c.GetKubeconfig(cluster)
						if err != nil {
							m.Text = fmt.Sprintf("oops: %s\n", err)

						} else {
							m.Text = fmt.Sprintf("*%s*:\n```%s```\n", cluster.Name, kc)
						}
					}
				}
				Send(ws, m)
			}(m)

		default:
			m.Text = "i have no clue what you are talking about...\n"
			Send(ws, m)
		}
	}
}
