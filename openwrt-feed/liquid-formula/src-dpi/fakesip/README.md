# FakeSIP

Disguise your UDP traffic as SIP protocol to evade DPI detection, using Netfilter Queue (NFQUEUE).

[[ 中文文档 ]](https://github.com/MikeWang000000/FakeSIP/wiki)


## Quick Start

```
fakesip -i eth0
```


## Usage

```
Usage: fakesip [options]

Interface Options:
  -a                 work on all network interfaces (ignores -i)
  -i <interface>     work on specified network interface

Payload Options:
  -b <file>          use UDP payload from binary file
  -u <uri>           use specified SIP URI

General Options:
  -0                 process outbound packets
  -1                 process inbound packets
  -4                 process IPv4 connections
  -6                 process IPv6 connections
  -d                 run as a daemon
  -k                 kill the running process
  -s                 enable silent mode
  -w <file>          write log to <file> instead of stderr

Port Filters:
  -p <ports>         comma-separated whitelist ports eq 53,80-1000
  -P <ports>         comma-separated blacklist ports eq 51820,51413

Advanced Options:
  -f                 skip firewall rules
  -g                 disable hop count estimation
  -m <mark>          fwmark for bypassing the queue
  -n <number>        netfilter queue number
  -r <repeat>        duplicate generated packets for <repeat> times
  -t <ttl>           TTL for generated packets
  -x <mask>          set the mask for fwmark
  -y <pct>           raise TTL dynamically to <pct>% of estimated hops
  -z                 use iptables commands instead of nft
```


## License

GNU General Public License v3.0
