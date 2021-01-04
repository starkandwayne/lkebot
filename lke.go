package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"time"

	"github.com/linode/linodego"
	"golang.org/x/oauth2"
)

type Status int
const (
	Live Status = iota
	Terminating
	Gone
)

type Cluster struct {
	ID   int
	Name string

	Region   string
	Instance string
	Size     int

	CreatedAt time.Time
	ExpiresAt time.Time

	Status Status

	seen bool
}

func (c *Cluster) String() string {
	if c.ExpiresAt.After(time.Now()) {
		left := fmt.Sprintf("%s", c.ExpiresAt.Sub(time.Now())+30*time.Minute)
		re := regexp.MustCompile(`\d+m.*`)
		left = re.ReplaceAllString(left, "")
		if left == "" {
			left += "less than 30m"
		}
		return fmt.Sprintf("*%s* [%d-node] _%s left_", c.Name, c.Size, left)
	}

	return fmt.Sprintf("*%s* [%d-node] _EXPIRED_", c.Name, c.Size)
}

func (c *Cluster) Renew(additional time.Duration) error {
	c.ExpiresAt = c.ExpiresAt.Add(additional)
	return nil
}

func (c *Cluster) Expire() error {
	c.ExpiresAt = time.Now()
	return nil
}

type Connection struct {
	api       linodego.Client
	clusters  map[int]*Cluster
	blacklist []string
	admins    []string
}

func Connect(token string) (*Connection, error) {
	c := linodego.NewClient(&http.Client{
		Transport: &oauth2.Transport{
			Source: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token}),
		},
	})

	return &Connection{
		api:       c,
		clusters:  map[int]*Cluster{},
		blacklist: make([]string, 0),
		admins:    make([]string, 0),
	}, nil
}

func (c *Connection) Blacklist(clusters ...string) {
	for _, s := range clusters {
		c.blacklist = append(c.blacklist, s)
	}
}

func (c *Connection) Blacklisted(cluster string) bool {
	for _, s := range c.blacklist {
		if s == cluster {
			return true
		}
	}
	return false
}

func (c *Connection) Count() int {
	return len(c.clusters)
}

func (c *Connection) add(cluster linodego.LKECluster, life time.Duration) *Cluster {
	existing := &Cluster{
		ID:     cluster.ID,
		Status: Live,
		Name:   cluster.Label,
		Region: cluster.Region,
		/* size and instance UNKNOWN at this time */
		CreatedAt: time.Now(),
	}
	if cluster.Created != nil {
		existing.CreatedAt = *cluster.Created
	}

	last := existing.CreatedAt
	if cluster.Updated != nil {
		last = *cluster.Updated
	} else if cluster.Created != nil {
		last = *cluster.Created
	}
	existing.ExpiresAt = last.Add(life)

	c.clusters[cluster.ID] = existing
	return existing
}

func (c *Connection) List() ([]*Cluster, error) {
	for k := range c.clusters {
		c.clusters[k].seen = false
	}

	l, err := c.api.ListLKEClusters(context.TODO(), nil)
	if err != nil {
		return nil, err
	}
	clusters := make([]*Cluster, 0)
	for _, cluster := range l {
		if c.Blacklisted(cluster.Label) {
			continue
		}

		n := 0
		if pools, err := c.api.ListLKEClusterPools(context.TODO(), cluster.ID, nil); err == nil {
			for _, pool := range pools {
				n += pool.Count
			}
		}

		existing, ok := c.clusters[cluster.ID]
		if !ok {
			existing = c.add(cluster, 8*time.Hour)
		}
		existing.seen = true
		existing.Size = n
		clusters = append(clusters, existing)
	}

	for k, v := range c.clusters {
		if !v.seen {
			delete(c.clusters, k)
		}
	}

	return clusters, nil
}

type Deployment struct {
	Name     string
	Region   string
	Size     int
	Instance string
	Version  string
	Lifetime time.Duration
}

func (c *Connection) Deploy(want Deployment) (*Cluster, error) {
	cluster, err := c.api.CreateLKECluster(context.TODO(), linodego.LKEClusterCreateOptions{
		K8sVersion: want.Version,
		Region:     want.Region,
		Label:      want.Name,
		NodePools: []linodego.LKEClusterPoolCreateOptions{
			linodego.LKEClusterPoolCreateOptions{
				Count: want.Size,
				Type:  want.Instance,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if cluster == nil {
		return nil, fmt.Errorf("failed to created cluster via linode api")
	}

	return c.add(*cluster, want.Lifetime), nil
}

func (c *Connection) Cleanup(what *Cluster) (bool, error) {
	kc, err := c.GetKubeconfig(what)
	if err != nil {
		return false, err
	}

	cmd := exec.Command("/usr/bin/cleanup")
	cmd.Env = append(cmd.Env, fmt.Sprintf("KUBECONFIG=%s", kc))

	b, err := cmd.CombinedOutput()
	fmt.Printf("command output:\n-----------\n%s\n------------\n", string(b))

	if err != nil {
		if status, ok := err.(*exec.ExitError); ok {
			if status.ExitCode() == 1 {
				return false, nil
			}
			return false, fmt.Errorf("cleanup script exited %d", status.ExitCode())
		}
		return false, err
	}

	return true, nil
}

func (c *Connection) Teardown(what *Cluster) error {
	what.Status = Terminating
	clean, err := c.Cleanup(what)
	if err != nil {
		return err
	}

	if clean {
		what.Status = Gone
		return c.api.DeleteLKECluster(context.TODO(), what.ID)
	}

	return nil
}

func (c *Connection) GetKubeconfig(what *Cluster) (string, error) {
	kc, err := c.api.GetLKEClusterKubeconfig(context.TODO(), what.ID)
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(kc.KubeConfig)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (c *Connection) Find(name string) (*Cluster, error) {
	for _, cluster := range c.clusters {
		if cluster.Name == name {
			return cluster, nil
		}
	}
	return nil, nil
}

func (c *Connection) Sweep() []string {
	cleaned := make([]string, 0)
	deadline := time.Now()
	for _, cluster := range c.clusters {
		if cluster.ExpiresAt.Before(deadline) {
			if cluster.Status == Live {
				cleaned = append(cleaned, cluster.Name)
			}
			go c.Teardown(cluster)
		}
	}
	return cleaned
}
