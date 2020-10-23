package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/andyleap/go-s3"
	"golang.org/x/crypto/bcrypt"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage"
)

type gitServe struct {
	s3        *s3.Client
	adminRepo *git.Repository

	storers    map[string]storer.Storer
	storerlock sync.RWMutex

	t transport.Transport

	tmpl *templater

	mu sync.Mutex
	c  *Config
}

type Config struct {
	Users map[string]*User
	Repos map[string]*RepoConfig
}

type User struct {
	Password []byte
}

type UserAccess struct {
	Access []string
}

type RepoConfig struct {
	Users map[string]UserAccess
}

func New(s3 *s3.Client) (*gitServe, error) {
	adminstorer := &S3Storage{s3: s3, base: "admin"}
	adminrepo, err := git.Open(adminstorer, nil)
	log.Println("opened repo ", err)
	if err == git.ErrRepositoryNotExists {
		adminrepo, err = git.Init(adminstorer, nil)
		log.Println("inited repo ", err)
		if err != nil {
			return nil, err
		}
	}

	gs := &gitServe{
		s3:        s3,
		adminRepo: adminrepo,
		storers:   map[string]storer.Storer{},
		tmpl: &templater{
			path: "templates",
		},
	}
	gs.t = server.NewServer(gs)
	return gs, nil
}

func (gs *gitServe) GetJsonnet(r *git.Repository, file string) (string, error) {
	adminref, err := gs.adminRepo.Reference(plumbing.Master, true)
	if err != nil {
		return "", err
	}

	c, err := gs.adminRepo.CommitObject(adminref.Hash())
	if err != nil {
		return "", err
	}

	tree, err := c.Tree()
	if err != nil {
		return "", err
	}

	return processJsonnet(tree, file)
}

func (gs *gitServe) getConfig() *Config {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	return gs.c
}

func (gs *gitServe) loadConfig() error {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	j, err := gs.GetJsonnet(gs.adminRepo, "config.jsonnet")
	if err != nil {
		p, _ := bcrypt.GenerateFromPassword([]byte("admin"), bcrypt.DefaultCost)
		gs.c = &Config{
			Users: map[string]*User{
				"admin": {
					Password: p,
				},
			},
			Repos: map[string]*RepoConfig{
				"admin": {
					Users: map[string]UserAccess{
						"admin": {
							Access: []string{"git-upload-pack", "git-receive-pack"},
						},
					},
				},
			},
		}
		return nil
	}

	c := &Config{}
	err = json.Unmarshal([]byte(j), &c)
	if err != nil {
		return err
	}
	gs.c = c
	return nil
}

func (gs *gitServe) Load(ep *transport.Endpoint) (storer.Storer, error) {
	rc := gs.getConfig().Repos[ep.Path]

	found := false
	u := rc.Users[ep.User]
	for _, a := range u.Access {
		if ep.Password == a {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("Not Authorized")
	}

	gs.storerlock.Lock()
	defer gs.storerlock.Unlock()
	if s, ok := gs.storers[ep.Path]; ok {
		return s, nil
	}
	s := &S3Storage{
		s3:   gs.s3,
		base: ep.Path,
	}

	_, err := git.Open(s, nil)
	if err == git.ErrRepositoryNotExists {
		_, err = git.Init(s, nil)
		if err != nil {
			return nil, err
		}
	}

	gs.storers[ep.Path] = s
	return gs.storers[ep.Path], nil
}

func main() {
	key, ok := os.LookupEnv("S3_KEY")
	if !ok {
		log.Fatal("Could not find S3_KEY, please assert it is set.")
	}
	secret, ok := os.LookupEnv("S3_SECRET")
	if !ok {
		log.Fatal("Could not find S3_SECRET, please assert it is set.")
	}
	client, _ := s3.NewClient(&s3.Client{
		AccessKeyID:     key,
		SecretAccessKey: secret,
		Domain:          "us-east-1.linodeobjects.com",
		Bucket:          "andyleap-git",
		UsePathBuckets:  true,
	})

	g, err := New(client)
	if err != nil {
		log.Fatal(err)
	}

	g.loadConfig()

	http.HandleFunc("/", g.git)
	http.ListenAndServe(":8080", nil)
}

func (gs *gitServe) index(rw http.ResponseWriter, req *http.Request) {
	c := gs.getConfig()

	publicRepos := []struct {
		Path string
		Repo *RepoConfig
	}{}

	for name, repo := range c.Repos {
		for _, access := range repo.Users["nobody"].Access {
			if access == "web" {
				publicRepos = append(publicRepos, struct {
					Path string
					Repo *RepoConfig
				}{name, repo})
			}
		}
	}

	gs.tmpl.Render("index.html", struct {
		PublicRepos []struct {
			Path string
			Repo *RepoConfig
		}
	}{
		PublicRepos: publicRepos,
	}, rw)
	return
}

func (g *gitServe) git(rw http.ResponseWriter, req *http.Request) {

	if req.URL.Path == "/" {
		g.index(rw, req)
		return
	}

	ep := &transport.Endpoint{}

	service := req.FormValue("service")
	p := req.URL.Path
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimSuffix(p, "/info/refs")
	if strings.HasSuffix(p, "/git-upload-pack") {
		service = "git-upload-pack"
		p = strings.TrimSuffix(p, "/git-upload-pack")
	} else if strings.HasSuffix(p, "/git-receive-pack") {
		service = "git-receive-pack"
		p = strings.TrimSuffix(p, "/git-receive-pack")
	}
	ep.Password = service

	if service == "" {
		ep.Password = "web"
		parts := strings.SplitN(p, "/blob/", 2)
		service = "web/blob"
		if len(parts) != 2 {
			parts = strings.SplitN(p, "/commit/", 2)
			service = "web/commit"
			if len(parts) != 2 {
				_, ok := g.getConfig().Repos[p]
				parts = []string{p, ""}
				service = "web/blob"
				if !ok {
					http.Error(rw, "bad request", 400)
					return
				}
			}
		}
		p = parts[0]
		ep.Host = parts[1]
	}

	log.Println(service, p)

	if user, pass, ok := req.BasicAuth(); ok {
		u, ok := g.getConfig().Users[user]
		if !ok {
			rw.Header().Set("WWW-Authenticate", "Basic")
			http.Error(rw, "unauthorized", 401)
			return
		}
		if bcrypt.CompareHashAndPassword(u.Password, []byte(pass)) != nil {
			rw.Header().Set("WWW-Authenticate", "Basic")
			http.Error(rw, "unauthorized", 401)
			return
		}
		ep.User = user
	} else {
		ep.User = "nobody"
	}

	ep.Path = p

	switch service {
	case "web/blob", "web/commit":
		s, err := g.Load(ep)
		if err != nil {
			rw.Header().Set("WWW-Authenticate", "Basic")
			http.Error(rw, "unauthorized", 401)
			return
		}
		refName := "HEAD"
		path := ""
		if ep.Host != "" {
			path = ep.Host
		}
		r, err := git.Open(s.(storage.Storer), nil)
		if err != nil {
			http.Error(rw, "bad request", 400)
			return
		}

		chash, err := r.ResolveRevision(plumbing.Revision(refName))
		if err != nil {
			http.Error(rw, "bad request", 400)
			return
		}

		c, err := r.CommitObject(*chash)
		if err != nil {
			http.Error(rw, "bad request", 400)
			return
		}

		if service == "web/commit" {
			//g.RenderCommit(c, rw, req)
			return
		}

		tree, err := c.Tree()
		if err != nil {
			http.Error(rw, "bad request", 400)
			return
		}

		if path == "" {
			g.RenderTree(tree, rw, req)
			return
		}

		e, err := tree.FindEntry(path)
		if err != nil {
			http.Error(rw, "bad request", 400)
			return
		}

		if e.Mode.IsFile() {
			b, err := r.BlobObject(e.Hash)
			if err != nil {
				http.Error(rw, "bad request", 400)
				return
			}
			g.RenderBlob(b, rw, req)
			return
		}

		t, err := r.TreeObject(e.Hash)
		if err != nil {
			http.Error(rw, "bad request", 400)
			return
		}
		g.RenderTree(t, rw, req)

	case "git-upload-pack":
		ups, err := g.t.NewUploadPackSession(ep, nil)

		if err != nil {
			rw.Header().Set("WWW-Authenticate", "Basic")
			http.Error(rw, "unauthorized", 401)
			return
		}
		if req.Method == http.MethodGet {
			advref, err := ups.AdvertisedReferences()
			if err != nil {
				http.Error(rw, err.Error(), 400)
				return
			}
			rw.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
			pl := pktline.NewEncoder(rw)
			pl.Encodef("# service=%s", service)
			pl.Encode()
			pl.Flush()
			advref.Encode(rw)
			return
		}

		upreq := packp.NewUploadPackRequest()

		buf, _ := ioutil.ReadAll(req.Body)
		fmt.Println(string(buf))

		err = upreq.Decode(bytes.NewReader(buf))
		if err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		fmt.Println(upreq)

		upresp, err := ups.UploadPack(req.Context(), upreq)
		if err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}
		rw.Header().Set("Content-Type", "application/x-"+service+"-result")
		upresp.Encode(rw)

	case "git-receive-pack":
		urp, err := g.t.NewReceivePackSession(ep, nil)
		if err != nil {
			rw.Header().Set("WWW-Authenticate", "Basic")
			http.Error(rw, "unauthorized", 401)
			return
		}
		if req.Method == http.MethodGet {
			advref, err := urp.AdvertisedReferences()
			if err != nil {
				http.Error(rw, err.Error(), 400)
				return
			}
			rw.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
			pl := pktline.NewEncoder(rw)
			pl.Encodef("# service=%s", service)
			pl.Encode()
			pl.Flush()
			advref.Encode(rw)
			return
		}
		rureq := packp.NewReferenceUpdateRequest()
		err = rureq.Decode(req.Body)
		if err != nil {
			http.Error(rw, err.Error(), 400)
			return
		}

		fmt.Println(rureq)

		rsresp, err := urp.ReceivePack(req.Context(), rureq)
		if err != nil {
			log.Println("Receive pack error", err)
			http.Error(rw, err.Error(), 400)
			return
		}
		rw.Header().Set("Content-Type", "application/x-"+service+"-result")
		rsresp.Encode(rw)

		g.loadConfig()
	}
}

type Entry struct {
	Name string
}

func (gs *gitServe) RenderTree(t *object.Tree, rw http.ResponseWriter, req *http.Request) {
	treeData := struct {
		Dirs  []Entry
		Files []Entry
	}{}

	for _, entry := range t.Entries {
		switch entry.Mode {
		case filemode.Dir:
			treeData.Dirs = append(treeData.Dirs, Entry{
				Name: entry.Name,
			})
		default:
			treeData.Files = append(treeData.Files, Entry{
				Name: entry.Name,
			})
		}
	}

	gs.tmpl.Render("tree.html", treeData, rw)
}

func (gs *gitServe) RenderBlob(b *object.Blob, rw http.ResponseWriter, req *http.Request) {
	rc, err := b.Reader()
	if err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	defer rc.Close()
	io.Copy(rw, rc)
}
