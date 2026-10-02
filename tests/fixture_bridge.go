package main

import (
 "encoding/json"
 "fmt"
 "net/http"
 "os"
 "strings"
)

func main() {
 mode:=os.Getenv("R7_FIXTURE_MODE")
 if mode=="crash" {
  fmt.Println(`[LITTLEP-BRIDGE] search message_id=SYNTH query="private question text" latency_ms=7 ok=true count=1 domains=[example.org]`)
  fmt.Println(`[LITTLEP-BRIDGE] rejected method=POST body_bytes=49 reason=private_question_text`)
  os.Exit(23)
 }
 if mode=="rotate" {
  fmt.Println("[LITTLEP-BRIDGE] unknown "+strings.Repeat("OVERSIZED_PRIVATE_SENTINEL",15000))
  for i:=0;i<25000;i++ { fmt.Println("[LITTLEP-BRIDGE] batch message_id="+strings.Repeat("A",120)+" phase=answer seq=2 chars=7") }
  fmt.Println("[LITTLEP-BRIDGE] search query=SECRET_SENTINEL latency_ms=8 ok=true")
 }
 http.HandleFunc("/health",func(w http.ResponseWriter,r *http.Request){
  w.Header().Set("Content-Type","application/json")
  _=json.NewEncoder(w).Encode(map[string]interface{}{"ok":true,"version":"0.7.0-source-ready","web_search":true})
 })
 if e:=http.ListenAndServe("127.0.0.1:"+os.Getenv("R7_PORT"),nil); e!=nil { os.Exit(2) }
}
