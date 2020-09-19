package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path"
	"sync"

	"github.com/andyleap/go-s3"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/storage"
)

type gitServe struct {
	s3        s3.Client
	adminRepo *git.Repository

	storers    map[string]storage.Storer
	storerlock sync.RWMutex
}

func New(s3 s3.Client) (*gitServe, error) {
	adminstorer := &S3Storage{s3: s3, base: "admin"}
	adminrepo, err := git.Open(adminstorer, nil)
	if err == git.ErrRepositoryNotExists {
		adminrepo, err = git.Init(adminstorer, nil)
		if err != nil {
			return nil, err
		}
	}

	gs := &gitServe{
		s3:        s3,
		adminRepo: adminrepo,
	}
	return gs, nil
}

func (gs *gitServe) Load(ep *transport.Endpoint) (storage.Storer, error) {
	adminref, err := gs.adminRepo.Reference(plumbing.Master, true)
	if err != nil {
		return nil, err
	}

	gs.adminRepo.CommitObject(adminref.Hash())
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

	//http.HandleFunc("/", g.git)
	http.ListenAndServe(":8080", nil)
}

func (g *gitServe) git(reposrv transport.Transport, rw http.ResponseWriter, req *http.Request) {

	ep, err := transport.NewEndpoint("/")
	if err != nil {
		http.Error(rw, err.Error(), 400)
		return
	}
	service := req.FormValue("service")
	if service == "" {
		service = path.Base(req.URL.Path)
	}
	switch service {
	case "git-upload-pack":
		ups, err := g.t.NewUploadPackSession(ep, nil)

		if err != nil {
			http.Error(rw, err.Error(), 400)
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
			http.Error(rw, err.Error(), 400)
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
			http.Error(rw, err.Error(), 400)
			return
		}
		rw.Header().Set("Content-Type", "application/x-"+service+"-result")
		rsresp.Encode(rw)
	}
}
