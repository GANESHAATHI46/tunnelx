package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<h1 style='color:#16834a;font-family:sans-serif;'>🎉 Success! TunnelX Local App is Working!</h1><p>Request Path: %s</p>", r.URL.Path)
	})
	log.Println("Sample local app running on http://127.0.0.1:8000 ...")
	if err := http.ListenAndServe("127.0.0.1:8000", nil); err != nil {
		log.Fatal(err)
	}
}
