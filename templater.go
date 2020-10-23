package main

import (
	"html/template"
	"io/ioutil"
	"net/http"
	"path/filepath"
)

type templater struct {
	path string
}

func (t templater) Render(name string, data interface{}, rw http.ResponseWriter) error {
	tmpl := template.New("")
	tdata, err := ioutil.ReadFile(filepath.Join(t.path, name))
	if err != nil {
		return err
	}
	tmpl.Parse(string(tdata))
	return tmpl.Execute(rw, data)
}
