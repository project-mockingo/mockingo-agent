package main

import (
	"fmt"
	"log"
	"net/http"
)

func main() {
	http.HandleFunc("GET /users", func(writer http.ResponseWriter, request *http.Request) {
		log.Printf("REAL %s %s", request.Method, request.URL.RequestURI())
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(writer, `[{"id":1,"name":"Ada"}]`)
	})
	http.HandleFunc("POST /orders", func(writer http.ResponseWriter, request *http.Request) {
		log.Printf("REAL %s %s", request.Method, request.URL.RequestURI())
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintln(writer, `{"id":"order-1"}`)
	})
	log.Println("real example application listening on http://127.0.0.1:8080")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", nil))
}
