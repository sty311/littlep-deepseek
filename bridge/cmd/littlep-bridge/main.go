package main

import (
	"flag"
	"littlep-deepseek/internal/bridge"
	"log"
)

func main() {
	path := flag.String("config", "config.json", "configuration file")
	flag.Parse()
	if err := bridge.Run(*path); err != nil {
		log.Fatal(err)
	}
}
