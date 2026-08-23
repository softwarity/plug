# Meerkat × plug - conclusions de conception (02/08/2026)

_Meerkat : future app-gateway (Go, produit de marché - pas une solution interne),
qui pilote plug. Ce fichier fige les conclusions de la session de conception du
02/08, pour ne pas re-dérouler le raisonnement à chaque fois._

## Le modèle retenu

**Identité.** Un utilisateur avec l'ability `dev` dépose sa clé publique via
Meerkat. La clé est associée à son nom et connue de plug : il s'authentifie sur
le port de l'agent, télécharge le CLI, et chaque session est **attribuée
nominativement**. (La clé unique embarquée dans le binaire disparaît à terme.)

**Exclusivité.** Un nom = une session. On n'autorise PAS deux devs à plugger le
même service - c'est déjà le comportement de plug (collision refusée par le
bail, qui porte l'origine du détenteur depuis le 02/08).

**On ne choisit pas, on informe.** Il n'y a pas de sélection d'override par
testeur. Toute personne entrant dans le cluster utilise l'état courant : les
services pluggés tels qu'ils sont. Meerkat rend cet état **visible par tous** :
quel service est pluggé, par qui, depuis quand. (Aujourd'hui cette situation
existe déjà - silencieusement et anonymement ; Meerkat ne la crée pas, il la
rend visible et attribuée.)

**Pas de rôle `tester`.** Puisque la consommation de l'état est indiscriminée,
un rôle côté consommation n'a pas de sens. Le contrôle porte sur la
*production* de l'état, à deux niveaux :

- **Par environnement** : « accepte-t-on des services pluggés ici ? » -
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
  plusieurs IP au service n'y change rien - il faudrait que l'application binde
  explicitement ses sockets sortants, donc du code applicatif.
- Un **résolveur par stack** (sous-réseau dédié, appartenance structurelle par
  préfixe) fonctionne - mais uniquement pour les services *dédiés* : un process
  partagé sert N stacks avec un seul resolv.conf et une seule route.
- **eBPF** (Odigos/Beyla : uprobes + corrélation par thread/goroutine,
  écriture du traceparent via bpf_probe_write_user) : corrélation heuristique -
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
ambiguïté - `(fpl-svc) → (fpl-svc2)` fonctionne).

## V2 différée : la sélection là où le client la permet

Le seul chemin correct est la **propagation applicative** de contexte. Pour un
produit de marché, ça devient un prérequis d'intégration - le même que TOUTE la
concurrence (Telepresence personal intercepts, Signadot sandboxes) : le marché a
convergé, ce n'est pas un défaut de Meerkat mais une propriété du problème.

- **Véhicule** : le standard W3C `baggage` (jamais un en-tête propriétaire) -
  propagé automatiquement par l'auto-instrumentation OpenTelemetry quand le
  client l'a (javaagent, NODE_OPTIONS… : réglage de déploiement, pas du code).
- **Détection, pas déclaration** : Meerkat sonde chaque chaîne (requête
  marquée ; la marque ressort-elle ?) et affiche par service « override profond
  disponible » ou « limité au premier saut ». Un override silencieusement
  ignoré est pire qu'un override refusé avec sa raison.
- **Aiguillage** : le takeover devient *sélectif* - le signpost devient un
  petit proxy L7, le déployé n'est plus parqué mais shadowé (il garde son nom
  de stack complet ; en k8s le Service repointe, le Deployment reste up).
  Requête sans marque → déployé ; marquée → la session du dev visé.
  HTTP/gRPC uniquement - assumé : l'intention « tester via l'UI » est HTTP,
  et on n'override pas un mongo par testeur.

## Implications côté plug (chantiers)

1. **Auth par clé par dev** : remplacer la clé embarquée ; provisionnement des
   clés par Meerkat ; l'utilisateur `get` (download anonyme) à repenser dans ce
   modèle. *Tranché le 22/08, voir la dernière section.*
2. **Attribution** : la brique existe - le bail porte `SSH_CLIENT` depuis le
   02/08. Avec les clés nominatives, l'origine devient un *nom*.
3. **API d'état** : exposer « qui plugge quoi depuis quand » (leases, signposts,
   origines) pour la page d'état Meerkat.
4. **Endpoint tunnel hébergé par la gateway**, on/off par environnement (la
   ligne roadmap existante) - c'est le mécanisme du « plug autorisé ici ou pas ».
5. Plus tard (V2) : le signpost-proxy L7 et le shadowing du déployé.

_Forme d'intégration tranchée le 22/08 : **un seul binaire**, Meerkat important
le paquet agent de plug. Voir « Un seul binaire » en fin de fichier, et ce que
cela implique pour les fichiers de déploiement de Meerkat._

## Ce que ce modèle préserve

Rien ne dépend du code des clients. La sélection par testeur reste une capacité
additive (détectée, jamais promise comme socle) - elle s'ajoute par-dessus sans
rien réécrire. Le datapath de plug (TUN, DNS, tunnel, versioning) est intouché.

## Conclusions de la session du 22/08 : l'authentification

Cette session tranche le chantier 1 ci-dessus. Rien n'est codé, ces lignes
figent les décisions et disent pourquoi les alternatives ont été écartées.

### Clés publiques, pas certificats signés par une CA

Le modèle du 02/08 tient : l'agent connaît une liste de clés publiques. Les
certificats SSH (une CA Meerkat signe la clé de chaque dev, l'agent ne connaît
que la CA) avaient un avantage réel, rendre l'agent capable d'authentifier
**sans joindre Meerkat**. Cet avantage ne vaut rien ici : Meerkat est la
gateway du cluster, donc sa disponibilité est un prérequis, pas une hypothèse
à protéger. Un cluster sans sa gateway n'a rien à offrir à un tunnel. Restent
alors les inconvénients du certificat : révocation seulement à l'expiration,
donc rotation courte et renouvellement à construire. Écarté.

Cas de bord assumé : déboguer la gateway elle-même avec plug pendant qu'elle
est en panne. Réel, mais pas de quoi porter une architecture.

### Deux conteneurs dans un pod : EXAMINE PUIS ECARTE le 22/08

**Conclusion périmée, conservée pour son raisonnement.** La décision finale est
le binaire unique (voir « Un seul binaire » plus bas). Ce qui suit dit pourquoi
le sidecar semblait préférable, et ce qu'il a fallu accepter en y renonçant.

L'argument était le privilège : l'agent tourne en root avec l'accès à la socket
Docker ou au token du ServiceAccount, tandis que Meerkat expose un port console
au monde. Une image unique offre cette surface à la façade publique. Et
`plug update` provoque un rolling restart de l'agent, qui emporte la gateway
avec lui.

Ce qui l'a écarté : **Swarm et Compose n'ont pas de pod**. Un conteneur compagnon
partageant localhost n'existe que sur Kubernetes, et plug doit marcher partout de
la même façon. Le coût du privilège est donc assumé (voir plus bas ce qui le
borne), et le redémarrage groupé devient une contrainte de publication : mettre à
jour l'agent implique de mettre à jour Meerkat.

### Où vivent les clés, selon le mode

Meerkat n'a **aucun droit sur le cluster** : seul l'agent en a. C'est ce qui
décide de l'emplacement, et ce qui a fait écarter l'idée que Meerkat écrive
lui-même dans l'orchestrateur.

| mode | source des clés | alimentée par |
|---|---|---|
| autonome (sans Meerkat) | annotation du Deployment de l'agent (Swarm : label du service) | enrôlement avec un code affiché dans les logs de l'agent |
| avec Meerkat | la base Meerkat, lue en loopback depuis le pod | l'UI Meerkat, ability `dev` |

L'annotation ne coûte aucun droit nouveau : `deployments: get, list, patch`
est déjà accordé pour que `plug update` déclenche son rolling restart. Et
aucun volume n'est jamais nécessaire : `preflight()` refuse de démarrer un
agent sans accès orchestrateur, donc un agent qui tourne a toujours de quoi
écrire quelque part.

Reste à trancher : l'agent interroge Meerkat en HTTP sur localhost à chaque
connexion (révocation instantanée, mais dépend de l'ordre de démarrage des
conteneurs), ou Meerkat dépose un fichier dans un volume partagé (robuste au
démarrage, propagation à la seconde). Un repli sur la dernière liste connue
réconcilie les deux.

### Le mode est un état du serveur, jamais gravé dans le client

Idée écartée : que l'agent serve un binaire marqué, privé de ses verbes de clé
en mode public. Trois raisons.

1. Un drapeau côté client n'est pas une sécurité. Ce qui protège, c'est que
   l'agent refuse une connexion non authentifiée. Un binaire modifié ou
   récupéré ailleurs contourne le drapeau sans effort.
2. Le cache de version casse. Le core est rangé sous `versions/<version>/plug`
   et vérifié à chaque lancement contre le digest annoncé par l'agent. Deux
   agents de même version servant deux binaires différents, c'est un chemin de
   cache pour deux contenus : qui travaille sur un cluster public et un cluster
   privé re-télécharge le core à chaque alternance.
3. Le mode change après coup. Activer Meerkat six mois après l'installation
   rendrait faux un drapeau posé à l'installation.

À la place : un mot de plus dans la réponse au verbe `version` (ou un verbe
`mode`). Les verbes existent toujours dans le binaire et interrogent l'agent.
`plug keygen` génère, ou répond que cet agent n'utilise pas l'authentification
par clé. Un seul binaire, un message qui reste vrai quand le cluster change.

### La paire de clés est générée par plug, à l'installation

Le script d'installation installe le binaire **avant** d'avoir besoin de la
clé : il appelle donc `plug keygen`, pas `ssh-keygen`. Pas de dépendance
externe, format maîtrisé, et le verbe sert ensuite à la rotation. Le code
d'enrôlement se saisit sur `/dev/tty`, comme `sudo` le fait déjà dans ce script
sous `| sh`.

Verbes à prévoir côté client, par profil (donc une identité par cluster, ce qui
tombe juste) :

- `plug keygen [-p profil]` : génère la paire dans `~/.plug/keys/<profil>` et
  écrit `key = ...` dans `~/.plug/<profil>.conf`.
- `plug pubkey [-p profil]` : affiche la publique, à coller dans l'UI Meerkat.
- `plug keygen --renew` : rotation.

Note : `~/.ssh/config` ne peut pas servir. plug n'appelle pas le client ssh du
système, il ouvre le canal avec `golang.org/x/crypto/ssh`, qui ne lit jamais ce
fichier. L'association profil vers clé vit dans le `.conf` de plug.

### Ce que l'authentification n'apporte pas

Le canal est déjà chiffré aujourd'hui, la clé partagée ne sert qu'à signer une
preuve pendant l'authentification. Le gain n'est donc pas la confidentialité :
c'est de savoir **qui** ouvre un tunnel, et de pouvoir révoquer quelqu'un qui
part. L'utilisateur `get` reste anonyme dans tous les cas, sinon plus personne
ne peut installer le client.

## Conclusions de la session du 22/08 (suite) : embarquer l'agent dans Meerkat

Le sidecar ne marche pas partout : Swarm et Compose n'ont pas de pod, donc pas
de conteneur compagnon partageant localhost. Comme une seule image reste
l'objectif, la piste retenue est d'embarquer le code de l'agent dans Meerkat.
Ci-dessous ce que cela implique réellement.

### plug reste autonome, sans authentification

Enoncé explicitement pour qu'aucune lecture future n'en doute :
`deploy/plug-k8s.yaml` et les stacks Swarm continuent de déployer un agent seul,
sans authentification, et c'est le mode par défaut. Meerkat est un contexte
d'exécution supplémentaire, jamais un prérequis.

### Ce qu'est l'agent, et pourquoi sshd est le sujet

L'agent n'est pas « un programme Go ». C'est **un serveur OpenSSH** dont le
helper Go n'est qu'une extension branchée en ForceCommand. Le helper exécute les
verbes (`serve-name`, `resolve`) ; il ne voit jamais passer un octet du tunnel.
Ce qui porte le trafic, c'est sshd, sur deux mécanismes du protocole :

- `direct-tcpip` : le CLI demande une connexion vers `service:port`, sshd
  l'ouvre depuis l'intérieur du cluster et relaie (`cli/.../transport.go`).
- `tcpip-forward` : pour `-s`, sshd écoute un port dans le conteneur et pousse
  chaque connexion vers le poste (`cli/.../expose.go`, `cl.Listen`), avec
  `GatewayPorts clientspecified` pour binder 0.0.0.0 et non la loopback.

Embarquer le helper sans traiter ce point donnerait un Meerkat qui exécute des
verbes pendant que plus personne ne répond en SSH.

### Le travail, en trois étapes

1. **Extraire le helper en paquet importable.** 3108 lignes hors tests, déjà en
   Go : les sortir de `package main`, remplacer les `fatal()` par des erreurs
   retournées. Découpage, pas réécriture.
2. **Fournir le serveur SSH.** Deux options, voir ci-dessous.
3. **Un point d'entrée** `agent.Start(ctx, Config{...})`, lancé en goroutine au
   démarrage de Meerkat, avec configuration et arrêt propre.

Point important : le CLI n'a **rien** à changer. Il parle SSH et se moque de qui
l'implémente en face, donc les deux mondes coexistent sans migration.

### sshd en sous-processus, ou serveur SSH en Go

| | `exec.Command(sshd)` | implémentation Go |
|---|---|---|
| à écrire | presque rien | 400 à 600 lignes, l'essentiel dans le remote forward |
| surface | OpenSSH, PAM, utilisateurs Unix, ForceCommand, helper **setuid root** | une bibliothèque, aucun utilisateur Unix, aucun setuid |
| authentification | fichier `authorized_keys` à synchroniser | une fonction Go |
| risque | éprouvé | code réseau critique à écrire |

`golang.org/x/crypto/ssh` fournit le côté serveur, et c'est déjà la bibliothèque
que le CLI utilise côté client.

Si l'implémentation Go est retenue, elle doit **remplacer sshd partout**, y
compris dans l'image autonome : deux implémentations serveur à maintenir serait
le pire des mondes. La suite e2e construite en août rend la bascule tenable :
dix-neuf cellules identiques, huit protocoles, trois orchestrateurs, trois OS,
plus les cellules de `-s`, de collision et de résilience. La réécriture serait
couverte dès la première exécution.

### Un fournisseur de clés, deux implémentations

Idée retenue, et elle remplace la notion de « deux modes » : l'agent n'a qu'un
seul chemin de code, il demande à une **source** si une clé est acceptée. Seule
la source change.

- Source par défaut, en mémoire, embarquée : répond toujours la clé commune.
  C'est le comportement d'aujourd'hui, et le code dit alors honnêtement ce qu'il
  fait, à savoir accepter une clé que tout le monde possède.
- Source Meerkat : les clés des devs, en base. La clé commune n'y figure pas,
  donc elle cesse d'être acceptée sans qu'aucun drapeau ne l'ait décidé.

La bascule est le remplacement d'une implémentation, pas une condition semée
dans le code. La source par défaut doit rester une implémentation en mémoire :
surtout pas un service mock qu'il faudrait lancer.

Cela rend caduques les questions ouvertes de la section précédente (push contre
pull, annotation contre loopback) dans le cas Meerkat : la vérification devient
un appel de fonction. Elles restent pertinentes pour le mode autonome avec
enrôlement, si ce mode est construit un jour.

### Le piège du versioning

Désactiver `plug update` ne suffit pas. Le CLI télécharge son **core** depuis
l'agent à chaque lancement (`ensureVersion`) : ce n'est pas la commande de mise
à jour, c'est le mécanisme de versioning de base. Meerkat doit donc embarquer et
servir les binaires du CLI, environ 40 Mo dans son image, et la version servie
doit correspondre au code agent embarqué. La contrepartie assumée : mettre à
jour l'agent implique de mettre à jour Meerkat.

### Droits de Meerkat sur le cluster

Une gateway qui observe son namespace est la norme, pas une exception : les
contrôleurs d'Ingress et les implémentations de Gateway API posent des watches
sur Services, EndpointSlices et routes. La frontière n'est donc pas lecture
contre rien, elle est ailleurs :

| droit | usage | position |
|---|---|---|
| `get/list/watch` services, endpoints, pods | découvrir et suivre l'état | oui, c'est le métier |
| `get` secrets | certificats TLS | seulement si Meerkat les sert |
| `create/update` services, deployments | modifier le cluster | à justifier au cas par cas |
| `create` roles, rolebindings | fabriquer des droits | non : Kubernetes exige de détenir ce qu'on accorde, donc une façade publique détiendrait tout en permanence |

Ce que la lecture rapporte : le chantier 3 (« qui plugge quoi ») devient
partiellement gratuit, puisque les Services au selector `app: plug` sont
visibles directement ; un watch met la page d'état à jour sans polling ; et
Meerkat peut détecter `argocd.argoproj.io/tracking-id` sur un Service pour
prévenir le dev **avant** qu'il ne perde une heure (voir la page CD & GitOps de
la doc, écrite le 22/08 après exactement ce scénario).

Garde-fous : un Role **par namespace** plutôt qu'un ClusterRole, lecture seule
tant qu'aucune écriture n'est nommée, et pas de `secrets` sans besoin.

### Le sens de la dépendance

plug reste le produit : son dépôt, sa CI, sa licence, son cycle de publication.
**Meerkat importe le paquet agent de plug**, jamais l'inverse. Deux binaires
compilés depuis des sources partagées, pas un binaire décliné en deux saveurs.

Formulé autrement : plug standalone n'est pas « Meerkat sans gateway ». Si la
flèche s'inversait, plug deviendrait un sous-produit de Meerkat, ce qui
contredit son autonomie posée plus haut.

### Le serveur SSH en Go a un intérêt propre, hors Meerkat

Trois gains indépendants de toute intégration, dont le dernier corrige une
faiblesse réelle du produit d'aujourd'hui.

**Le setuid disparaît.** `plug-agent` est en mode `4755` uniquement parce qu'il
s'exécute sous l'utilisateur SSH `plug` et doit atteindre la socket Docker ou le
token du ServiceAccount. Dans un serveur Go, le processus détient déjà ces
accès : un binaire setuid root en moins, sur la cible la plus exposée.

**La configuration sshd cesse d'exister.** Dix-neuf lignes générées par `echo`
dans le Dockerfile (donc jamais testées), les utilisateurs Unix, les
ForceCommand, et l'installation d'`openssh-server` avec son retry sur trois
tentatives, présent parce que ce paquet a déjà fait échouer des builds.

**L'épinglage de la host key deviendrait vrai.** Aujourd'hui il ne protège de
rien, et le code l'assume (`cli/internal/tunnel/transport.go:456`) : l'agent
régénère sa host key à chaque démarrage (`ssh-keygen -A` dans l'entrypoint),
donc le CLI accepte silencieusement tout changement en notant « agent host key
changed (agent restart?) - re-pinned ». Quelqu'un prenant la place de l'agent
serait accueilli par ce message. C'est aussi ce qui force le
`StrictHostKeyChecking=no` dans toutes les commandes d'installation. Un serveur
Go peut persister sa host key là où il persiste déjà le reste, et l'épinglage
se met à valoir quelque chose.

### Garder le protocole SSH, remplacer seulement l'implémentation

Question posée : a-t-on besoin de SSH, ou d'un protocole plus simple adapté au
cas ? Réponse : du protocole oui, de son implémentation non.

Ce que SSH apporte ici n'est pas le chiffrement, c'est le multiplexage de
canaux et deux primitives qui correspondent exactement au besoin
(`direct-tcpip`, `tcpip-forward`). Un protocole maison devrait fournir la même
chose : TLS, plus un multiplexeur, plus ces deux primitives réécrites. Même
travail, sans vingt ans de spécification.

Et un point tranche seul : **l'installation repose sur le client `ssh` du
système** (`ssh get@host install | sh`). Aucun prérequis à installer, sur les
trois OS. Un transport maison le casserait, ou obligerait à garder SSH rien que
pour cela, donc à maintenir les deux.

Piste notée sans être recommandée : le jour où Meerkat expose déjà un port TLS,
faire passer le tunnel dessus par ALPN éviterait d'exposer un port SSH séparé.
Se décide plus tard, n'oblige à rien maintenant.

### Un seul binaire : la décision, qui remplace toutes les variantes ci-dessus

**C'est la conclusion qui vaut.** Meerkat importe le paquet agent de plug et les
deux ne forment qu'un exécutable. Pas de sidecar, pas de deuxième conteneur, pas
de processus compagnon : plug devient une partie intégrée de Meerkat.

Ce que cela balaie, et c'est l'essentiel : toutes les questions de frontière
disparaissent. Plus de `localhost` partagé, plus de volume commun, plus de push
contre pull, plus de latence de propagation, plus de RBAC à accorder à Meerkat
pour qu'il écrive là où l'agent lit. Vérifier une clé devient un appel de
fonction, et le vault de Meerkat est directement accessible au code de l'agent.

Ce que cela demande côté plug : le code de l'agent doit devenir **importable**.
Aujourd'hui c'est un `package main` dont les `fatal()` appellent `os.Exit`. Il
faut un paquet qui retourne des erreurs et expose un point d'entrée. Découpage,
pas réécriture. Le mode autonome reste `plug-agent serve`, qui construit le
fournisseur par défaut et appelle exactement le code que Meerkat appellera.

Le coût assumé : un processus exposé au monde par le port console détient aussi
le token du ServiceAccount. Ce qui le borne, et rend le choix défendable, c'est
que le Role de l'agent est minuscule et namespacé - gérer des Services, patcher
un Deployment, dans un seul namespace. Ce n'est pas la clé du cluster. Le jour
où quelqu'un voudra y ajouter des droits « pendant qu'on y est », c'est là qu'il
faudra rouvrir la question, pas avant.

### Ce que l'intégration change dans les fichiers de déploiement de Meerkat

Conséquence directe et facile à sous-estimer : **le déploiement de Meerkat doit
réclamer ce que réclamait celui de l'agent**. Concrètement, en reprenant
`deploy/plug-k8s.yaml` :

- Kubernetes : un `ServiceAccount`, un `Role` (services : get/list/create/
  delete/update/patch ; deployments : get/list/patch) et le `RoleBinding` qui va
  avec, plus le port SSH ajouté au Service de Meerkat (un seul Service, trois
  ports : console, dataplane, ssh).
- Swarm et Compose : la socket Docker montée dans le conteneur Meerkat, et sur
  Swarm le placement sur un nœud **manager**.

Ce n'est pas anodin pour un produit de marché : une gateway qui exige des droits
sur le cluster est une gateway plus difficile à faire accepter, et beaucoup de
ses utilisateurs n'auront jamais besoin de plug. D'où la règle qui découle :

**La fonctionnalité doit être désactivable, et désactivée par défaut.** Meerkat
sans plug ne demande aucun droit cluster et n'ouvre aucun port SSH. Le manifeste
« avec plug » est un supplément documenté, que l'on applique en connaissance de
cause. Sans cela, on impose une élévation de privilèges à tous les utilisateurs
de la gateway pour une fonction que certains n'activeront jamais.

**Et un défaut de conception que cela révèle.** `preflight()` refuse aujourd'hui
de démarrer un agent privé d'accès orchestrateur, et c'est le bon comportement
pour un agent dédié : un conteneur en bonne santé qui échouerait au premier `-s`
cacherait un montage manquant. Mais **fatal pour la gateway entière, c'est
inacceptable** : un droit RBAC oublié ferait refuser de démarrer un Meerkat par
ailleurs parfaitement fonctionnel. Dans le mode intégré, l'absence d'accès doit
désactiver la fonction plug et le dire, pas arrêter le processus. C'est une
modification à prévoir dans l'extraction en paquet : le point d'entrée retourne
une erreur, et c'est l'appelant qui décide si elle est fatale.

### Etape 6, après la bascule : image distroless et binaires compressés

Rien d'autre que sshd ne retient l'image sur Alpine : le helper est du Go pur,
pas un seul `exec.Command` dans ses 3108 lignes. Ne restent que trois éléments
non-Go, et le remplacement de sshd les emporte tous les trois : sshd lui-même,
`entrypoint.sh` (qui devient un mode `plug-agent serve`), et les 201 lignes de
`serve-binary` (que le serveur Go absorbe : servir un fichier, calculer un
SHA-256, encoder en base64 sont trois appels de bibliothèque standard).

Base `gcr.io/distroless/static` et non `scratch` : le helper interroge le
registre Docker en HTTPS pour `plug update`, donc il lui faut les certificats
racine, plus `/etc/passwd` pour ne pas tourner en root.

De 98,6 Mo à environ 62 Mo, puis vers 30 Mo si les binaires servis sont stockés
gzippés et décompressés en flux (le SHA-256 se calcule sur le flux, sans fichier
temporaire). Mais le gain qui compte n'est pas là : **plus de shell dans le
conteneur**, donc plus d'interpréteur à trouver pour qui obtiendrait une
exécution de code, et plus de binaire setuid root. C'est ce qui se défend
devant une équipe sécurité ; la taille, elle, se remarque au premier pull.

A faire APRES la bascule : changer le serveur et la base de l'image en même
temps rendrait un échec indiagnosticable.

**Mode « online » écarté**, pour que personne ne le repropose : faire télécharger
les binaires depuis Internet plutôt que les embarquer casserait quatre choses.
Beaucoup de clusters n'ont pas Internet (proxy, air-gapped, registre interne
seul autorisé) alors que l'agent est aujourd'hui autosuffisant une fois l'image
tirée. Les binaires ne sont pas un extra : le CLI télécharge son core depuis
l'agent **à chaque lancement**, donc une image sans eux ne fait plus tourner de
session. Le verbe `digest` perdrait son sens, puisqu'il faudrait faire confiance
à une source externe pour un binaire exécuté avec privilège. Et la promesse
« publié égale testé », établie par la construction des binaires dans le même
build que l'image, serait rompue. La compression donne le même gain de taille
sans rien de tout cela.
