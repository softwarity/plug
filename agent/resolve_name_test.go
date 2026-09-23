package agent

import "testing"

// `resolve` takes a name the long way, rabbitmq.shop.svc.cluster.local, because
// that is how a pod's environment names its peers and the CLI asks the cluster
// about exactly that name before minting it. The verbs that create a Service
// keep the one-label rule, and neither accepts what is not a hostname.
func TestResolveTakesAClusterLongName(t *testing.T) {
	for _, n := range []string{"rabbitmq", "rabbitmq.shop.svc", "rabbitmq.shop.svc.cluster.local", "a.b"} {
		if !resolveArgOK([]string{"resolve", n}) {
			t.Errorf("resolve rejects %q", n)
		}
	}
	for _, n := range []string{"", ".", "rabbitmq.", ".rabbitmq", "rabbitmq..shop", "Rabbit.shop", "-a.b", "a-.b", "a b.c", "a.b:5672"} {
		if resolveArgOK([]string{"resolve", n}) {
			t.Errorf("resolve accepts %q", n)
		}
	}
	if resolveArgOK([]string{"resolve"}) || resolveArgOK([]string{"resolve", "a", "b"}) {
		t.Error("resolve takes exactly one name")
	}
	if nameRe.MatchString("rabbitmq.shop.svc") {
		t.Error("nameRe must keep a Service to one label")
	}
}
