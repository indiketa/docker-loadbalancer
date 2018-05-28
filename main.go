package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"golang.org/x/net/context"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"
	"io/ioutil"
)

type Endpoint struct {
	Name string
	IP   string
	Port int
}

var haproxy *os.Process
var haproxyDesiredState bool

func main() {

	ctx := context.Background()
	cli, err := client.NewClientWithOpts()
	if err != nil {
		panic(err)
	}

	evt := make(chan int, 1)

	go func(evt chan int) {

		lastHash := "-1"

		filters := filters.NewArgs(filters.KeyValuePair{Key: "label", Value: "lb.enable=Y"})

		for {
			containers, err := cli.ContainerList(ctx, types.ContainerListOptions{Filters: filters})
			if err != nil {
				panic(err)
			}

			group := make(map[int][]Endpoint)

			// group containers by publish port
			for index := range containers {
				readContainerNetwork(containers[index], group)
			}

			// order endpoints by name
			for _, value := range group {
				sort.Slice(value, func(i, j int) bool {
					return strings.Compare(value[i].Name, value[j].Name) < 0
				})
			}

			if len(group) > 0 {

				// generate config & hash
				config, hash, _ := generateHAProxyConfig(group)

				// compare last hash with new generated hash
				if strings.Compare(lastHash, hash) != 0 {
					fmt.Println("Backends changed. Reconfiguring haproxy with:")
					for port, backends := range group {
						fmt.Println("\nPublish port", port, "TCP")
						for _, backend := range backends {
							fmt.Println("  |- Backend", backend.Name, "at", backend.IP, "port", backend.Port)
						}
					}

					writeHAProxyConfig(config)
					signalOrStartHAProxy(evt)
					lastHash = hash
				}
			} else {
				stopHAProxyIfStarted()
				lastHash = "-1"
			}

			time.Sleep(5 * time.Second)
		}
	}(evt)

	<-evt
	fmt.Println("auto-lb terminated")

}

func readContainerNetwork(container types.Container, group map[int][]Endpoint) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Println("Container ", container.Names[0][1:], " skipped, due to error: ", r)
		}
	}()

	publish, err := strconv.Atoi(container.Labels["lb.publish"])
	if err != nil {
		panic("Label lb.publish not found or not convertible to integer")
	}

	target, err := strconv.Atoi(container.Labels["lb.target"])
	if err != nil {
		panic("Label lb.target not found or not convertible to integer")
	}

	for m := range container.NetworkSettings.Networks {
		group[publish] = append(group[publish], Endpoint{container.Names[0][1:], container.NetworkSettings.Networks[m].IPAddress, target})
	}

	return nil
}

func generateHAProxyConfig(group map[int][]Endpoint) (config string, hash string, err error) {

	 conf := `
global
   stats timeout 30s
   daemon

defaults
    mode                    tcp
    log                     global
    option                  httplog
    option                  dontlognull
    option 				  	http-server-close
    option                  redispatch
    retries                 3
    timeout http-request    10s
    timeout queue           1m
    timeout connect         10s
    timeout client          1m
    timeout server          1m
    timeout http-keep-alive 10s
    timeout check           10s
	maxconn                 3000

{{range $key, $value := .}}frontend port_{{$key}}
    bind *:{{$key}}
    mode tcp
    option tcplog
    timeout client  10800s
    default_backend port_{{$key}}_backends

backend port_{{$key}}_backends
    mode tcp
    balance leastconn
    timeout server  10800s
	{{range .}}server {{.Name}} {{.IP}}:{{.Port}}
	{{end}}
{{end}}
`

	if _, err := os.Stat("/haproxy.tmpl"); err == nil {
		b, err := ioutil.ReadFile("/haproxy.tmpl")
		if err != nil {
			conf = string(b)
		}
	}

	t := template.Must(template.New("conf").Parse(conf))


	buf := new(bytes.Buffer)
	err = t.Execute(buf, group)
	if err != nil {
		panic(err)
	}

	config = buf.String()

	hasher := md5.New()
	hasher.Write([]byte(config))
	hash = hex.EncodeToString(hasher.Sum(nil))

	return
}

func writeHAProxyConfig(config string) error {
	f, err := os.Create("/usr/local/etc/haproxy/haproxy.cfg")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	n3, err := f.WriteString(config)
	fmt.Println("Wrote ", n3, " bytes in ", f.Name())

	return nil
}

func stopHAProxyIfStarted() {
	haproxyDesiredState = false
	fmt.Println("No container found with label lb.enable=Y")
	if haproxy != nil {
		fmt.Println("Stopping haproxy")
		haproxy.Signal(syscall.SIGTERM)
	}
}

func signalOrStartHAProxy(evt chan int) {
	haproxyDesiredState = true
	if haproxy == nil {

		if binary, err := exec.LookPath("haproxy"); err == nil {
			go func() {
				restarts := 0
				for restarts < 5 {
					var procAttr os.ProcAttr
					procAttr.Files = []*os.File{os.Stdin, os.Stdout, os.Stderr}
					fmt.Println("Starting haproxy lb...")
					haproxy, err = os.StartProcess(binary, []string{"-W", "-db", "-f", "/usr/local/etc/haproxy/haproxy.cfg"}, &procAttr)
					if err != nil {
						panic(err)
					}
					fmt.Println("Started haproxy with pid", haproxy.Pid)

					haproxy.Wait()
					if haproxyDesiredState {
						fmt.Println("haproxy died, restarting in 10 seconds")
						time.Sleep(10 * time.Second)
						haproxy.Kill()
						haproxy = nil
						restarts++
					} else {
						fmt.Println("haproxy stopped.")
						haproxy = nil
						return
					}

				}
				fmt.Println("haproxy has restarted", restarts, "times. Terminating loadbalancer")
				evt <- -1
			}()

		} else {
			fmt.Println("haproxy executable not found on system")
			evt <- -1
		}

	} else {
		fmt.Println("Signaling HAProxy with SIGUSR2, pid", haproxy.Pid)
		haproxy.Signal(syscall.SIGUSR2)
	}

}
