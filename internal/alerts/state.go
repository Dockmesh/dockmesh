package alerts

import "time"

// maxTargetsPerHost bounds the targets one rule tracks on one host. An
// agent can report any container name it likes, so without a cap a
// stream of unique names would grow memory and alert_history forever.
const maxTargetsPerHost = 500

// target is what an alert is about: a container, mount, host, backup job
// or domain. name is the value stored in alert_history.container_name;
// hostID is empty for the kinds that belong to no host.
type target struct {
	hostID, hostName, name string
}

type ruleHost struct {
	rule int64
	host string
}

// tkey identifies one alert: a rule on one target of one host. The host
// id is part of the key so same-named targets on two hosts stay apart.
type tkey struct {
	rule   int64
	host   string
	target string
}

func keyOf(ruleID int64, t target) tkey { return tkey{ruleID, t.hostID, t.name} }

func (k tkey) ruleHost() ruleHost { return ruleHost{k.rule, k.host} }

// targetMap holds per-target state grouped by rule and host, so the cap
// check is a length lookup.
type targetMap[V any] map[ruleHost]map[string]V

func (m targetMap[V]) get(k tkey) (V, bool) {
	v, ok := m[k.ruleHost()][k.target]
	return v, ok
}

// full reports whether k is new and its rule already tracks
// maxTargetsPerHost targets on k's host.
func (m targetMap[V]) full(k tkey) bool {
	inner := m[k.ruleHost()]
	_, ok := inner[k.target]
	return !ok && len(inner) >= maxTargetsPerHost
}

// set stores v unless that would exceed the cap; it reports success.
func (m targetMap[V]) set(k tkey, v V) bool {
	if m.full(k) {
		return false
	}
	inner := m[k.ruleHost()]
	if inner == nil {
		inner = map[string]V{}
		m[k.ruleHost()] = inner
	}
	inner[k.target] = v
	return true
}

func (m targetMap[V]) del(k tkey) {
	rh := k.ruleHost()
	inner := m[rh]
	delete(inner, k.target)
	if len(inner) == 0 {
		delete(m, rh)
	}
}

// keysOf lists the keys of one rule on every host.
func (m targetMap[V]) keysOf(ruleID int64) []tkey {
	var out []tkey
	for rh, inner := range m {
		if rh.rule != ruleID {
			continue
		}
		for t := range inner {
			out = append(out, tkey{rh.rule, rh.host, t})
		}
	}
	return out
}

func (m targetMap[V]) hasRule(ruleID int64) bool {
	for rh := range m {
		if rh.rule == ruleID {
			return true
		}
	}
	return false
}

// size counts every tracked target.
func (m targetMap[V]) size() int {
	n := 0
	for _, inner := range m {
		n += len(inner)
	}
	return n
}

// lastRow is the newest alert_history row of a one-shot target, which
// repeats inside the cooldown are counted on.
type lastRow struct {
	id int64
	at time.Time
}
