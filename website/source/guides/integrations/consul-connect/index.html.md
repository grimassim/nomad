---
layout: "guides"
page_title: "Consul Connect"
sidebar_current: "guides-integrations-consul-connect"
description: |-
  Learn how to use Nomad with Consul Connect to enable secure service to service communication
---

# Consul Connect

~> **Note** This guide describes a new feature available in the [Nomad 0.10.0
   Beta release][download] of Nomad.

[Consul Connect](https://www.consul.io/docs/connect/index.html) provides
service-to-service connection authorization and encryption using mutual
Transport Layer Security (TLS). Applications can use sidecar proxies in a
service mesh configuration to automatically establish TLS connections for
inbound and outbound connections without being aware of Connect at all.

# Nomad with Consul Connect Integration

Nomad integrates with Consul to provide secure service-to-service communication
between Nomad jobs and task groups. In order to support Consul Connect, Nomad
adds a new networking mode for jobs that enables tasks in the same task group to
share their networking stack. With a few changes to the job specification, job
authors can opt into Connect integration. When Connect is enabled, Nomad will
launch a proxy alongside the application in the job file. The proxy (Envoy)
provides secure communication with other applications in the cluster.

Nomad job specification authors can use Nomad's Consul Connect integration to
implement [service segmentation](https://www.consul.io/segmentation.html) in a
microservice architecture running in public clouds without having to directly
manage TLS certificates. This is transparent to job specification authors as
security features in Connect continue to work even as the application scales up
or down or gets rescheduled by Nomad.

# Nomad Consul Connect Example

The following section walks through an example to enable secure communication
between a web dashboard and a backend counting service. The web dashboard and
the counting service are managed by Nomad. Nomad additionally configures Envoy
proxies to run along side these applications. The dashboard is configured to
connect to the counting service via localhost on port 9001. The proxy is managed
by Nomad, and handles mTLS communication to the counting service.

## Prerequisites

### Consul

Connect integration with Nomad requires [Consul 1.6 or
later.](https://releases.hashicorp.com/consul/1.6.0/) The Consul agent can be
run in dev mode with the following command:

**Note**: Nomad's Connect integration requires Consul in your `$PATH`

```sh
$ consul agent -dev 
```

### Nomad

Nomad must schedule onto a routable interface in order for the proxies to
connect to each other. The following steps show how to start a Nomad dev agent
configured for Connect.

```sh
$ sudo nomad agent -dev-connect
```

### CNI Plugins

Nomad uses CNI plugins to configure the network namespace used to secure the
Consul Connect sidecar proxy. All Nomad client nodes using network namespaces
must have CNI plugins installed.

The following commands install CNI plugins:

```sh
$ curl -L -o cni-plugins.tgz https://github.com/containernetworking/plugins/releases/download/v0.8.1/cni-plugins-linux-amd64-v0.8.1.tgz
$ sudo mkdir -p /opt/cni/bin
$ sudo tar -C /opt/cni/bin -xzf cni-plugins.tgz
```

## Run the Connect-enabled Services

Once Nomad and Consul are running, submit the following Connect-enabled services
to Nomad by copying the HCL into a file named `connect.nomad` and running:
`nomad run connect.nomad`

```hcl
 job "countdash" {
   datacenters = ["dc1"]
   group "api" {
     network {
       mode = "bridge"
     }

     service {
       name = "count-api"
       port = "9001"

       connect {
         sidecar_service {}
       }
     }

     task "web" {
       driver = "docker"
       config {
         image = "hashicorpnomad/counter-api:v1"
       }
     }
   }

   group "dashboard" {
     network {
       mode = "bridge"
       port "http" {
         static = 9002
         to     = 9002
       }
     }

     service {
       name = "count-dashboard"
       port = "9002"

       connect {
         sidecar_service {
           proxy {
             upstreams {
               destination_name = "count-api"
               local_bind_port = 8080
             }
           }
         }
       }
     }

     task "dashboard" {
       driver = "docker"
       env {
         COUNTING_SERVICE_URL = "http://${NOMAD_UPSTREAM_ADDR_count_api}"
       }
       config {
         image = "hashicorpnomad/counter-dashboard:v1"
       }
     }
   }
 }
```

The job contains two task groups: an API service and a web frontend.

### API Service

The API service is defined as a task group with a bridge network:

```hcl
   group "api" {
     network {
       mode = "bridge"
     }

     ...
   }
```

Since the API service is only accessible via Consul Connect, it does not define
any ports in its network. The service stanza enables Connect:

```hcl
   group "api" {
     ...

     service {
       name = "count-api"
       port = "9001"

       connect {
         sidecar_service {}
       }
     }

     ...
   }
```

The `port` in the service stanza is the port the API service listens on. The
Envoy proxy will automatically route traffic to that port inside the network
namespace.

### Web Frontend

The web frontend is defined as a task group with a bridge network and a static
forwarded port:

```hcl
   group "dashboard" {
     network {
       mode ="bridge"
       port "http" {
         static = 9002
         to     = 9002
       }
     }

     ...
   }
```

The `static = 9002` parameter requests the Nomad scheduler reserve port 9002 on
a host network interface. The `to = 9002` parameter forwards that host port to
port 9002 inside the network namespace.

This allows you to connect to the web frontend in a browser by visiting
`http://<host_ip>:9002` as show below:

[![Count Dashboard][count-dashboard]][count-dashboard]

The web frontend connects to the API service via Consul Connect:

```hcl
     service {
       name = "count-dashboard"
       port = "9002"

       connect {
         sidecar_service {
           proxy {
             upstreams {
               destination_name = "count-api"
               local_bind_port = 8080
             }
           }
         }
       }
     }
```

The `upstreams` stanza defines the remote service to access (`count-api`) and
what port to expose that service on inside the network namespace (`8080`).

The web frontend is configured to communicate with the API service with an
environment variable:

```hcl
       env {
         COUNTING_SERVICE_URL = "http://${NOMAD_UPSTREAM_ADDR_count_api}"
       }
```

The web frontend is configured via the `$COUNTING_SERVICE_URL`, so you must
interpolate the upstream's address into that environment variable. Note that
dashes (`-`) are converted to underscores (`_`) in environment variables so
`count-api` becomes `count_api`.

## Limitations

 - The `consul` binary must be present in Nomad's `$PATH` to run the Envoy
   proxy sidecar on client nodes.
 - Consul Connect Native is not yet supported.
 - Consul Connect HTTP and gRPC checks are not yet supported.
 - Consul ACLs are not yet supported.
 - Only the Docker, exec, and raw exec drivers support network namespaces and
   Connect.
 - Variable interpolation for group services and checks are not yet supported.
 - Consul Connect and network namespaces are only supported on Linux.


[count-dashboard]: /assets/images/count-dashboard.png
[download]: https://releases.hashicorp.com/nomad/0.10.0-beta1/
