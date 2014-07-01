package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/fsouza/go-dockerclient"
)

const (
	PERSIST_DIR = "containers"
)

func envopt(name, def string) string {
	if env := os.Getenv(name); env != "" {
		return env
	}
	return def
}

func marshal(obj interface{}) []byte {
	bytes, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		log.Println("marshal:", err)
	}
	return bytes
}

func supervise(client *docker.Client, config *ConfigStore) {
	events := make(chan *docker.APIEvents)
	if err := client.AddEventListener(events); err != nil {
		log.Fatalln("[fatal] failed to subscribe to docker events")
	}
	for event := range events {
		if event.Status == "die" {
			container, err := client.InspectContainer(event.ID)
			if err != nil {
				log.Println("supervisor: container destroyed too quickly, skipping", event.ID)
				continue
			}

			name := container.Name[1:]

			conf, ok := config.Get(name)
			if !ok {
				continue
			}

			hostConfig := container.HostConfig

			if err := client.RemoveContainer(docker.RemoveContainerOptions{ID: container.ID}); err != nil {
				log.Println("supervisor: unable to remove container:", err)
			}

			newContainer, err := client.CreateContainer(docker.CreateContainerOptions{
				Name:   name,
				Config: conf,
			})
			if err != nil {
				log.Println("supervisor: unable to create container:", err)
				continue
			}

			if err := client.StartContainer(newContainer.ID, hostConfig); err != nil {
				log.Println("supervisor: unable to start container:", err)
			}
		}
	}
	log.Fatalln("[fatal] supervisor loop closed unexpectedly")
}

func main() {
	persistDir := envopt("PERSIST", PERSIST_DIR)
	endpoint := envopt("DOCKER_HOST", "unix:///var/run/docker.sock")
	port := envopt("PORT", "8080")

	client, err := docker.NewClient(endpoint)
	if err != nil {
		log.Fatalf("[fatal] failed to connect to docker: %s\n", err)
	}

	var persister Persister
	if _, err := os.Stat(persistDir); os.IsNotExist(err) {
		log.Printf("[warn] persist dir doesn't exist, not going to persist.")
	} else {
		persister = DirectoryPersister(persistDir)
	}

	config := NewConfigStore(persister)
	if err := config.Load(); err != nil {
		log.Printf("[warn] failed to load from persist dir: %v", err)
	}

	go supervise(client, config)

	http.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")

		if path == "" {
			switch r.Method {
			case "GET":
				list := make([]string, 0)
				for k, _ := range config.Copy() {
					list = append(list, k)
				}
				rw.Write(marshal(list))
			case "POST":
				if err := r.ParseForm(); err != nil {
					http.Error(rw, err.Error(), http.StatusBadRequest)
					return
				}

				name := strings.Trim(r.Form.Get("id"), "/")
				if name == "" {
					http.Error(rw, "Bad request", http.StatusBadRequest)
					return
				}

				if _, ok := config.Get(name); ok {
					rw.Header().Set("Location", "/"+name)
					rw.WriteHeader(http.StatusSeeOther)
					return
				}

				container, err := client.InspectContainer(name)
				if err != nil {
					http.Error(rw, err.Error(), http.StatusBadRequest)
					return
				}

				config.Add(container.Name[1:], container.Config)

				rw.Header().Set("Location", "/"+name)
				rw.WriteHeader(http.StatusCreated)
			default:
				http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
			}
		} else {
			conf, ok := config.Get(path)
			if !ok {
				http.Error(rw, "Not found", http.StatusNotFound)
				return
			}

			switch r.Method {
			case "GET":
				rw.Write(marshal(conf))
			case "DELETE":
				config.Remove(path)
			default:
				http.Error(rw, "Method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
