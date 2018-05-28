# docker-loadbalancer
Automatic haproxy-based docker load balancer. 

This container implements a container load balancer (containers running in a same host). Load balancer is configured automatically.

## Use case
Imagine you have 5 containers of ```nginx``` and you want to balance the incoming traffic between them, you need a loadbalancer. This loadbalancer is autoconfigured reading target container labels.

If you have any questions, please open an issue.

## Usage
Create a simple web application and annotate with labels:

```
docker run -d \
         --rm \
         --label "lb.enable=Y"  \
         --label "lb.publish=80" \
         --label "lb.target=80" \
         --name web1 \
         nginx:alpine
```
- Label ```lb.enable=Y``` Makes this container eligible for load balancer.
- Label ```lb.publish=80``` Indicates the port published by the lb.
- Label ```lb.target=80``` Indicates the port targeted to the container.

Instantiate the load balancer:

```
docker run \
        -d \
        --net host \
        -v /var/run/docker.sock:/var/run/docker.sock \
        --name lb \
        indiketa/docker-loadbalancer
```
The loadbalancer is autoconfigured and serving ```web1``` container directly. (you can use the bridge network, but then you have to publish frontend ports when launching loadbalancer container). 

Logs from loadbalancer:
```
$ docker logs lb
Backends changed. Reconfiguring haproxy with:

Publish port 80 TCP
Backend web1 at 172.17.0.2 , port 80
Wrote  793  bytes in  /usr/local/etc/haproxy/haproxy.cfg
Starting haproxy lb...
Started HAProxy with pid 12
```

Add another webserver:

```
docker run -d \
         --rm \
         --label "lb.enable=Y"  \
         --label "lb.publish=80" \
         --label "lb.target=80" \
         --name web2 \
         nginx:alpine
```
Check if loadbalancer is reconfigured:
```
$ docker logs lb
...
Backends changed. Reconfiguring haproxy with:

Publish port 80 TCP
Backend web1 at 172.17.0.2 , port 80
Backend web2 at 172.17.0.3 , port 80
Wrote  820  bytes in  /usr/local/etc/haproxy/haproxy.cfg
Signaling HAProxy with SIGUSR2, pid 12
```
All works.  Simple but effective haproxy load balancer. 
Haproxy is configured to balance the traffic using tcp mode (not http), you can balance anything.


## Real-life usage
I use this container to bypass the docker swarm mode routing mesh (mess). Each node has a instance of the load balancer, services are deployed like:

```
docker service create \
        --name nginx \
        --replicas 3 \
        --constraint 'node.labels.host  == sauron' \ 
        --container-label 'lb.enable=Y' \
        --container-label 'lb.publish=803' \
        --container-label 'lb.target=80' \
        nginx:alpine

```
The constraint is a host selector. 

## haproxy config template
Mount  ```/haproxy.tmpl``` to override default haproxy configuration:

```
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
```


## Changes
```main.go``` contains all the source, Exec `compile.sh` to generate the load balancer executable, and then the image.

