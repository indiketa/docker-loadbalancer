package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"golang.org/x/net/context"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/template"
	"time"
)

type Endpoint struct {
	Name string
	IP   string
	Port int
}

type GroupKey struct {
	Port int
	IP string
	Ssl  string
}

type ServiceConfiguration struct {
	Publish  GroupKey
	Backends []Endpoint
}

type WholeConfiguration struct {
	Services  []ServiceConfiguration
	StatsPort int
	Stats     string
}

type HaProxyTemplateModel struct {
	Services  []ServiceConfiguration
	Stats     string
	StatsPort int
	PidFile   string
	SockFile  string
}

var haproxyBinary string
var lastHash = "-1"
var statsPort = -1
var containerCheckTime = 5
var haproxyPidFile = "/tmp/haproxy.pid"
var haproxySock = "/tmp/haproxy.sock"
var noServicesPrinted = false

const haproxyConfig = "/usr/local/etc/haproxy/haproxy.cfg"

func main() {

	readEnvironmentConfiguration()
	startHAProxyIdleInstance()

	exit_chan := make(chan int, 1)
	signal_chan := make(chan os.Signal, 1)
	signal.Notify(signal_chan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	go func() {
		<-signal_chan
		log.Println("Exit signal received")
		if pid, process := findRunningHAProxyPid(); pid > 0 {
			log.Println("Sending 0x9 (SIGKILL) to HAProxy pid ", pid)
			process.Signal(syscall.SIGKILL)
		}
		exit_chan <- 0
	}()

	go func() {
		ctx := context.Background()
		cli, err := client.NewClientWithOpts()
		if err != nil {
			panic(err)
		}

		for {
			services := readServices(cli, ctx)
			if len(services) == 0 {
				if !noServicesPrinted {
					log.Println("No container found with label lb.enable=si")
					noServicesPrinted = true
				}
			} else {
				noServicesPrinted = false
			}

			conf := WholeConfiguration{StatsPort: statsPort, Services: services}
			config, hash, _ := generateHAProxyConfig(conf)
			if strings.Compare(lastHash, hash) != 0 {
				printCurrentServices(conf)
				applyConfiguration(config, hash)
			}
			time.Sleep(time.Duration(containerCheckTime) * time.Second)
		}
	}()

	code := <-exit_chan
	log.Println("auto-lb terminated")
	os.Exit(code)

}

func startHAProxyIdleInstance() {
	var err error
	if haproxyBinary, err = exec.LookPath("haproxy"); err == nil {
		conf := WholeConfiguration{StatsPort: statsPort, Services: make([]ServiceConfiguration, 0)}
		config, hash, _ := generateHAProxyConfig(conf)
		applyConfiguration(config, hash)
	} else {
		log.Fatal("haproxy executable not found ")
	}
}

func applyConfiguration(config, hash string) {
	writeFile(config, haproxyConfig)
	startNewHAProxy()
	lastHash = hash
}

func readEnvironmentConfiguration() {
	statsPort = readEnvInteger("LB_STATS_PORT")
	if statsPort > 0 {
		log.Println("HAProxy statistics port is", statsPort)
	}

	containerCheckTime = readEnvInteger("CHECK_TIME")
	if containerCheckTime <= 0 {
		containerCheckTime = 5
	}
	log.Println("Container refresh interval check is", containerCheckTime, "seconds")

	if len(os.Getenv("HAPROXY_PID_FILE")) > 0 {
		haproxyPidFile = os.Getenv("HAPROXY_PID_FILE")
	}

	if len(os.Getenv("HAPROXY_SOCK_FILE")) > 0 {
		haproxySock = os.Getenv("HAPROXY_SOCK_FILE")
	}

}

func readEnvInteger(name string) (retval int) {
	strValue := os.Getenv(name)
	retval = -1

	if len(strValue) != 0 {
		stat, err := strconv.Atoi(strValue)
		if err == nil {
			retval = stat
		} else {
			panic("Label " + name + " port not convertible to integer: " + strValue)
		}
	}
	return
}

func printCurrentServices(whole WholeConfiguration) {
	log.Println("Backends change dectected. Reconfiguring haproxy with:")
	for _, service := range whole.Services {
		proto := "HTTP"
		if len(service.Publish.Ssl) > 0 {
			proto = "SSL"
		}
		log.Println("")
		log.Println("Publish port", service.Publish.Port, proto, service.Publish.Ssl)
		for _, backend := range service.Backends {
			log.Println("  |- Backend", backend.Name, "at", backend.IP, "port", backend.Port)
		}
	}
}

func readServices(cli *client.Client, ctx context.Context) []ServiceConfiguration {

	group := make(map[GroupKey][]Endpoint)

	filters := filters.NewArgs(filters.KeyValuePair{Key: "label", Value: "lb.enable=Y"})
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{Filters: filters})
	if err != nil {
		panic(err)
	}

	// group containers by publish port
	for _, val := range containers {
		processContainer(val, group)
	}

	services := make([]ServiceConfiguration, 0)

	// order endpoints by name
	for key, value := range group {
		sort.Slice(value, func(i, j int) bool {
			return strings.Compare(value[i].Name, value[j].Name) < 0
		})
		services = append(services, ServiceConfiguration{Backends: value, Publish: key})
	}

	// order services by publish port asc
	sort.Slice(services, func(i, j int) bool {
		return services[i].Publish.IP + " " + strconv.Itoa(services[i].Publish.Port) <  services[j].Publish.IP + " " + strconv.Itoa(services[j].Publish.Port)
	})

	return services
}

func processContainer(container types.Container, group map[GroupKey][]Endpoint) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Println("Container", container.Names[0][1:], "skipped due to error: ", r)
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

	dst_address := container.Labels["lb.dst_addr"]

	key := GroupKey{Port: publish, IP:dst_address}

	if len(container.Labels["lb.ssl"]) != 0 {
		sslFile := container.Labels["lb.ssl"]
		if _, err := os.Stat(sslFile); os.IsNotExist(err) {
			panic("Label lb.ssl pem file does not exist: " + sslFile)
		} else {
			key.Ssl = sslFile
		}
	}

	for m := range container.NetworkSettings.Networks {
		group[key] = append(group[key], Endpoint{container.Names[0][1:], container.NetworkSettings.Networks[m].IPAddress,target})
	}

	return nil
}

func generateHAProxyConfig(whole WholeConfiguration) (config string, hash string, err error) {

	conf := `
global
   daemon
   stats socket {{$.SockFile}} mode 600 expose-fd listeners level user
   stats timeout 30s 
   pidfile {{$.PidFile}}
   log /dev/log local0 debug

defaults
    mode                    http
    log                     global
    option                  httplog
    option                  dontlognull
    option                  http-server-close
    option                  redispatch
    option                  forwardfor
    option                  originalto
    compression algo        gzip
    compression type        text/css text/html text/javascript application/javascript text/plain text/xml application/json
    retries                 3
    timeout http-request    10s
    timeout queue           1m
    timeout connect         10s
    timeout client          1m
    timeout server          1m
    timeout http-keep-alive 10s
    timeout check           10s
    maxconn                 3000{{if .Stats}}

listen stats
    bind *:{{.StatsPort}}
    stats enable
    stats hide-version
    stats refresh 5s
    stats show-node
    stats uri  /{{end}}

{{range $_, $value := .Services}}frontend port_{{$value.Publish.IP}}_{{$value.Publish.Port}}
    bind {{if $value.Publish.IP}}{{$value.Publish.IP}}{{else}}*{{end}}:{{$value.Publish.Port}}{{if $value.Publish.Ssl}} ssl crt {{$value.Publish.Ssl}}{{end}}
    default_backend port_{{$value.Publish.IP}}_{{$value.Publish.Port}}_backends
    rspdel ^ETag:.*

backend port_{{$value.Publish.IP}}_{{$value.Publish.Port}}_backends
    balance leastconn
    stick-table type ip size 200k expire 520m    
    stick on src
    {{range $value.Backends}}server {{.Name}} {{.IP}}:{{.Port}} 
	{{end}}
{{end}}
`

	if _, err := os.Stat("/haproxy.tmpl"); err == nil {
		b, err := ioutil.ReadFile("/haproxy.tmpl")
		if err == nil {
			conf = string(b)
		}
	}

	t := template.Must(template.New("conf").Parse(conf))

	buf := new(bytes.Buffer)

	stats := ""
	if whole.StatsPort > 0 {
		stats = "Y"
	}

	model := HaProxyTemplateModel{Services: whole.Services,
		Stats:     stats,
		StatsPort: whole.StatsPort,
		PidFile:   haproxyPidFile,
		SockFile:  haproxySock}

	err = t.Execute(buf, model)
	if err != nil {
		panic(err)
	}

	config = buf.String()

	//log.Println(config)

	hasher := md5.New()
	hasher.Write([]byte(config))
	hash = hex.EncodeToString(hasher.Sum(nil))

	return
}

func writeFile(config, name string) error {
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	n3, err := f.WriteString(config)
	log.Println("Wrote", n3, "bytes to", f.Name())
	return err
}

func findRunningHAProxyPid() (pid int, process *os.Process) {
	pid = -1
	if file, err := os.Open(haproxyPidFile); err == nil {
		defer file.Close()
		if scanner := bufio.NewScanner(file); scanner.Scan() {
			firstLine := scanner.Text()

			if pid, err = strconv.Atoi(string(firstLine)); err != nil {
				pid = -1
				log.Println(err)
			}
		}

		if pid > 0 {
			if process, err = os.FindProcess(pid); err == nil {
				if process.Signal(syscall.Signal(0x0)) != nil { // el signal 0 no fa res, pero dona error si el pid no existeix
					pid = -1
				}
			} else {
				pid = -1
			}
		}
	}

	return
}

func startNewHAProxy() {

	args := make([]string, 0)
	args = append(args, "-W", "-f", haproxyConfig)

	if pid, _ := findRunningHAProxyPid(); pid > 0 {
		args = append(args, "-x", haproxySock, "-sf", strconv.Itoa(pid))
	}

	log.Println("Starting new HAProxy instance: ", haproxyBinary, strings.Join(args, " "))

	procAttr := &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	if currentHAProxy, err := os.StartProcess(haproxyBinary, args, procAttr); err == nil {
		go func(process *os.Process) {
			process.Wait()
			log.Println("Master HAProxy started with pid", currentHAProxy.Pid, "has finished")
		}(currentHAProxy)
		time.Sleep(1 * time.Second)
	} else {
		log.Fatal("Error ocurred while starting a new HAProxy instance", err)
	}
}
