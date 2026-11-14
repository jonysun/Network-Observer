#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ip.h>
#include <linux/ipv6.h>
#include <linux/tcp.h>
#include <linux/udp.h>
#include <bpf/bpf_helpers.h>

#define IPPROTO_TCP 6
#define IPPROTO_UDP 17
#define IP_OFFSET 0x1fff
#define bpf_ntohs(x) __builtin_bswap16(x)
#define bpf_htons(x) __builtin_bswap16(x)

struct flow_key {
    __u8 family;
    __u8 protocol;
    __u8 direction;
    __u8 pad;
    __u8 source[16];
    __u8 destination[16];
    __u16 source_port;
    __u16 destination_port;
};

struct flow_value {
    __u64 bytes;
    __u64 packets;
    __u64 last_seen_ns;
};

struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
    __type(key, struct flow_key);
    __type(value, struct flow_value);
} flow_counts SEC(".maps");

static __always_inline int count_flow(struct __sk_buff *skb, __u8 direction) {
    void *data = (void *)(long)skb->data;
    void *end = (void *)(long)skb->data_end;
    struct ethhdr *eth = data;
    if ((void *)(eth + 1) > end) return 1;
    __u16 type = bpf_ntohs(eth->h_proto);
    __u64 offset = sizeof(*eth);
    if (type == ETH_P_8021Q || type == ETH_P_8021AD) {
        struct {
            __u16 tci;
            __u16 proto;
        } *vlan = data + offset;
        if ((void *)(vlan + 1) > end) return 1;
        type = bpf_ntohs(vlan->proto);
        offset += sizeof(*vlan);
    }

    struct flow_key key = {};
    key.direction = direction;
    if (type == ETH_P_IP) {
        struct iphdr *ip = data + offset;
        if ((void *)(ip + 1) > end || ip->ihl < 5 || ip->protocol != IPPROTO_TCP && ip->protocol != IPPROTO_UDP) return 1;
        if (ip->frag_off & bpf_htons(IP_OFFSET)) return 1;
        __u64 ihl = (__u64)ip->ihl * 4;
        if ((void *)ip + ihl + sizeof(struct udphdr) > end) return 1;
        key.family = 4;
        key.protocol = ip->protocol;
        __builtin_memcpy(&key.source[12], &ip->saddr, 4);
        __builtin_memcpy(&key.destination[12], &ip->daddr, 4);
        struct udphdr *ports = (void *)ip + ihl;
        key.source_port = bpf_ntohs(ports->source);
        key.destination_port = bpf_ntohs(ports->dest);
    } else if (type == ETH_P_IPV6) {
        struct ipv6hdr *ip6 = data + offset;
        if ((void *)(ip6 + 1) > end || ip6->nexthdr != IPPROTO_TCP && ip6->nexthdr != IPPROTO_UDP) return 1;
        if ((void *)(ip6 + 1) + sizeof(struct udphdr) > end) return 1;
        key.family = 6;
        key.protocol = ip6->nexthdr;
        __builtin_memcpy(key.source, &ip6->saddr, 16);
        __builtin_memcpy(key.destination, &ip6->daddr, 16);
        struct udphdr *ports = (void *)(ip6 + 1);
        key.source_port = bpf_ntohs(ports->source);
        key.destination_port = bpf_ntohs(ports->dest);
    } else {
        return 1;
    }

    __u64 now = bpf_ktime_get_ns();
    struct flow_value initial = { .bytes = skb->len, .packets = 1, .last_seen_ns = now };
    struct flow_value *value = bpf_map_lookup_elem(&flow_counts, &key);
    if (!value) {
        bpf_map_update_elem(&flow_counts, &key, &initial, BPF_NOEXIST);
        value = bpf_map_lookup_elem(&flow_counts, &key);
    }
    if (value) {
        __sync_fetch_and_add(&value->bytes, skb->len);
        __sync_fetch_and_add(&value->packets, 1);
        value->last_seen_ns = now;
    }
    return 1;
}

SEC("cgroup_skb/egress") int count_egress(struct __sk_buff *skb) { return count_flow(skb, 1); }
SEC("cgroup_skb/ingress") int count_ingress(struct __sk_buff *skb) { return count_flow(skb, 0); }
char LICENSE[] SEC("license") = "GPL";
