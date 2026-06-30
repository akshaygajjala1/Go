package main

import (
	"fmt"
	"net/http"
	"encoding/json"
)

type User struct {
	Name string `json:"name`
	ID int `json:"id`
}	

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /read", readHandler)
	mux.HandleFunc("GET /write", writeHandler)
	http.ListenAndServe(":8080", mux)
}

func writeHandler(w http.ResponseWriter, r *http.Request) {
	user := User{Name: "test", ID: 1}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
	fmt.Print("write(get)")
}

func readHandler(w http.ResponseWriter, r *http.Request) {
	var user User
	json.NewDecoder(r.Body).Decode(&user)
	w.WriteHeader(http.StatusOK)
	fmt.Print("read(post)")
}

