# docker-loadbalancer
Automatic haproxy-based docker load balancer. 

This container implements a container load balancer (only for containers running in a single host). The load balancer configures itself automatically.

## Use case
Imagine you have (in a single host) 5 containers of ```nginx``` and you want to balance the incoming traffic between them, you need a loadbalancer. This loadbalancer is autoconfigured reading target container labels.

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


## Changes
```main.go``` file contains the source code of the (stupid) configurator, run `compile.sh` to generate a load balancer image: First ```main.go``` is compiled (using a container) and then loadbalancer image is built.

