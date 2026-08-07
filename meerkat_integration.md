# Meerkat × plug — conclusions de conception (02/08/2026)

_Meerkat : future app-gateway (Go, produit de marché — pas une solution interne),
qui pilote plug. Ce fichier fige les conclusions de la session de conception du
02/08, pour ne pas re-dérouler le raisonnement à chaque fois._

## Le modèle retenu

**Identité.** Un utilisateur avec l'ability `dev` dépose sa clé publique via
Meerkat. La clé est associée à son nom et connue de plug : il s'authentifie sur
le port de l'agent, télécharge le CLI, et chaque session est **attribuée
nominativement**. (La clé unique embarquée dans le binaire disparaît à terme.)

**Exclusivité.** Un nom = une session. On n'autorise PAS deux devs à plugger le
même service — c'est déjà le comportement de plug (collision refusée par le
bail, qui porte l'origine du détenteur depuis le 02/08).

**On ne choisit pas, on informe.** Il n'y a pas de sélection d'override par
testeur. Toute personne entrant dans le cluster utilise l'état courant : les
services pluggés tels qu'ils sont. Meerkat rend cet état **visible par tous** :
quel service est pluggé, par qui, depuis quand. (Aujourd'hui cette situation
existe déjà — silencieusement et anonymement ; Meerkat ne la crée pas, il la
rend visible et attribuée.)

**Pas de rôle `tester`.** Puisque la consommation de l'état est indiscriminée,
un rôle côté consommation n'a pas de sens. Le contrôle porte sur la
*production* de l'état, à deux niveaux :

- **Par environnement** : « accepte-t-on des services pluggés ici ? » —
  dev : oui, prod : non. Concrètement : Meerkat expose ou non l'endpoint
  tunnel. En prod l'interdiction est une *absence* (rien à contourner, rien à
  auditer). C'est mot pour mot la ligne de la roadmap publique : « the gateway
  hosts the tunnel endpoint and turns it on and off dynamically ».
- **Par personne** : l'ability `dev` (déposer sa clé, plugger).

**Traçabilité gratuite.** Meerkat connaissant l'état à tout instant, un rapport
de bug peut être estampillé avec la composition exacte du cluster au moment du
test (« reproduit contre fpl-svc2@alice + fpl-ui@bob »).

**Évolutions compatibles, non construites** : réservation time-boxée (« le
staging bascule sur l'état d'Alice pour 15 min », piloté depuis l'UI, file
d'attente visible) ; politique d'environnement plus riche que on/off (fenêtres,
TTL de session).

## Pourquoi pas la sélection d'override par testeur (V1)

Le scénario voulu : le testeur choisit « le contexte d'Alice », charge l'UI, la
chaîne standard `gateway → fpl-svc (déployé) → fpl-svc2 (pluggé Alice)` route
vers la session d'Alice.

**Fait incompressible** : pour router ainsi, une information doit traverser le
service *partagé* (`fpl-svc`), et aucune couche sous l'application ne peut
relier sa requête entrante à son appel sortant. Vérifié sous tous les angles :

- Le **DNS** ne porte aucune identité (pas d'émetteur dans le protocole).
- L'**IP source** d'une requête DNS n'est pas héritée de la connexion entrante :
  c'est un socket neuf, source choisie par la route vers le résolveur (démontré
  sur le cluster : deux IP réseau, requête DNS émise depuis 127.0.0.1). Donner
  plusieurs IP au service n'y change rien — il faudrait que l'application binde
  explicitement ses sockets sortants, donc du code applicatif.
- Un **résolveur par stack** (sous-réseau dédié, appartenance structurelle par
  préfixe) fonctionne — mais uniquement pour les services *dédiés* : un process
  partagé sert N stacks avec un seul resolv.conf et une seule route.
- **eBPF** (Odigos/Beyla : uprobes + corrélation par thread/goroutine,
  écriture du traceparent via bpf_probe_write_user) : corrélation heuristique —
  acceptable pour du tracing, disqualifiante pour du routage (l'erreur route un
  humain au mauvais backend, silencieusement). Plus DaemonSet privilégié,
  Linux only, fragilité par version de runtime. Écarté, y compris pour la
  cartographie (la sonde applicative fait mieux pour cent fois moins cher).
- **Corrélation temporelle** (sidecar qui attribue les appels sortants pendant
  le traitement d'une requête marquée) : fausse dès qu'il y a du parallélisme.
  Écartée.

**Ce qui marche sans prérequis client** : le premier saut (le testeur atteint
directement ce qui est pluggé via la gateway) et les chaînes de services
pluggés (un service pluggé est *dédié*, sa vue par session/stack est sans
ambiguïté — `(fpl-svc) → (fpl-svc2)` fonctionne).

## V2 différée : la sélection là où le client la permet

Le seul chemin correct est la **propagation applicative** de contexte. Pour un
produit de marché, ça devient un prérequis d'intégration — le même que TOUTE la
concurrence (Telepresence personal intercepts, Signadot sandboxes) : le marché a
convergé, ce n'est pas un défaut de Meerkat mais une propriété du problème.

- **Véhicule** : le standard W3C `baggage` (jamais un en-tête propriétaire) —
  propagé automatiquement par l'auto-instrumentation OpenTelemetry quand le
  client l'a (javaagent, NODE_OPTIONS… : réglage de déploiement, pas du code).
- **Détection, pas déclaration** : Meerkat sonde chaque chaîne (requête
  marquée ; la marque ressort-elle ?) et affiche par service « override profond
  disponible » ou « limité au premier saut ». Un override silencieusement
  ignoré est pire qu'un override refusé avec sa raison.
- **Aiguillage** : le takeover devient *sélectif* — le signpost devient un
  petit proxy L7, le déployé n'est plus parqué mais shadowé (il garde son nom
  de stack complet ; en k8s le Service repointe, le Deployment reste up).
  Requête sans marque → déployé ; marquée → la session du dev visé.
  HTTP/gRPC uniquement — assumé : l'intention « tester via l'UI » est HTTP,
  et on n'override pas un mongo par testeur.

## Implications côté plug (chantiers)

1. **Auth par clé par dev** : remplacer la clé embarquée ; provisionnement des
   clés par Meerkat ; l'utilisateur `get` (download anonyme) à repenser dans ce
   modèle.
2. **Attribution** : la brique existe — le bail porte `SSH_CLIENT` depuis le
   02/08. Avec les clés nominatives, l'origine devient un *nom*.
3. **API d'état** : exposer « qui plugge quoi depuis quand » (leases, signposts,
   origines) pour la page d'état Meerkat.
4. **Endpoint tunnel hébergé par la gateway**, on/off par environnement (la
   ligne roadmap existante) — c'est le mécanisme du « plug autorisé ici ou pas ».
5. Plus tard (V2) : le signpost-proxy L7 et le shadowing du déployé.

## Ce que ce modèle préserve

Rien ne dépend du code des clients. La sélection par testeur reste une capacité
additive (détectée, jamais promise comme socle) — elle s'ajoute par-dessus sans
rien réécrire. Le datapath de plug (TUN, DNS, tunnel, versioning) est intouché.
