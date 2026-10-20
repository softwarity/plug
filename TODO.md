# plug - TODO / plan de travail

_État : 20 octobre 2026, **2.31.0 publiée**. L'historique des livraisons vit
dans `RELEASE_NOTES.md` et la roadmap publique dans
`docs/src/app/pages/roadmap.component.ts` ; ce fichier ne garde que ce qui est
**ouvert**, vérifié contre le code à cette date. La liste des cellules e2e est
`scripts/ci/check-e2e-cells.sh` (31 aujourd'hui), pas ce fichier._

## 🟡 Défauts connus, petits

- [ ] **`DaemonAlive` dit « aucun daemon » quand il ne peut pas lire le lock**
      (`cli/internal/tun/graft_darwin.go`) : tout échec d'`OpenFile` rend
      `false`, `permission denied` compris. Un binaire sans setuid (build
      local, copie dans `/tmp`) croit qu'aucun daemon ne tourne, en démarre un
      second qui échoue contre le premier, et l'utilisateur lit « cannot start
      the plug daemon ». Correctif : distinguer « lock libre » de « lock
      illisible » et dire, sans privilège, qu'un daemon tourne et que ce
      binaire n'a pas le setuid pour le rejoindre. La sonde Windows (SCM) a la
      même forme : un échec d'accès rend aussi `false`.
- [ ] **Live mount, deux manques** : (1) SQLite/GeoPackage sur SMB : les
      verrous de plage sont faibles, et les montages Linux et `--dockerrun`
      passent `nobrl` (aucun verrou) ; plug n'avertit pas. (2) Le refus TCC
      « Volumes réseau » de macOS est nommé juste après le montage
      (`cli/mount_darwin.go`), pas par `plug doctor`.

## 🟡 Observé une fois, inexpliqué

À regarder si ça revient ; aucune récidive dans les runs en échec depuis.

- [ ] **`TestADialThatHangsHoldsNeitherTheOtherClusterNorASecondDial`** (17/10,
      `Test (macos-latest)`, run 39045322501) : « the cluster was dialled again
      after its tunnel came up (2 dials) ». 200 passages sur 200 en local. Un
      tunnel installé puis jugé mort au tick suivant, ou un tick qui lit la
      carte avant l'installation.
- [ ] **`TestPidForLocalPortSelf`** (18/10, Ubuntu, run 39134301017) : la
      recherche dans la table des sockets de `/proc` n'a rien répondu pour le
      port du test lui-même, alors que le test la tient pour garantie sur un
      runner. C'est le mécanisme d'attribution des flux sous Linux.
- [ ] **Une session qui démarre sans jamais lancer la commande** (06/09, premier
      run du soak) : processus vivant (16 Mo, 6 threads, contre 30 Mo et 13
      pour une session saine), boucle jamais exécutée, la signature d'un
      launcher qui n'a pas monté son datapath. Le soak échoue désormais à 20 s
      si aucun tour n'est sorti et garde le stderr de plug en artefact.

## 🟡 Hors CI

- [ ] **Détection du portail captif sur Linux** : darwin et Windows ont leur
      collecteur (`cli/captive_darwin.go`, `cli/captive_windows.go`), Linux non
      (`captive_other.go` rend `known: false`, doctor se tait). `resolv.conf`
      ne suffit pas : il faut distinguer ce que DHCP a offert de ce qui est
      configuré (`nmcli dev show`, `resolvectl status` ou le bail dhclient
      selon la distro), et le remède doit nommer les deux sens, comme sur
      darwin.
- [ ] **Windows sous VPN d'entreprise** : non prouvé sur un vrai client (macOS
      l'est avec GlobalProtect). Couvert en CI par le selftest « fake VPN »
      (×3 OS), et la NRPT est lue (`scopedResolversNRPT`). Reste hors CI : le
      split-tunnel, la MTU/fragmentation, et les clients qui interceptent le
      DNS en `127.0.0.1` (`pickUpstreamsTraced` écarte l'adaptateur loopback
      logiciel, pas une adresse 127.0.0.1 posée sur un adaptateur réel). Un
      poste Windows avec un vrai client VPN, puis un banc d'une demi-heure.
- [ ] **Linux sous VPN d'entreprise : ni prouvé ni écarté**. Le risque est plus
      faible (le split DNS passe par systemd-resolved, prouvé par le selftest),
      mais aucun banc n'existe.
- [ ] **Le soak couvre la durée, pas la charge** (`.github/workflows/soak.yml`,
      4 h chaque lundi contre l'image publiée, trafic léger) : restent les gros
      transferts (une fuite proportionnelle au volume lui échapperait) et le
      sleep/wake, qui ne se teste pas sur un runner.
- [ ] **Couverture arm64 : 17 cellules sur 31, OPTIONNEL**. La jambe arm64
      tourne avec `full: false`. Ce que les cellules absentes exercent (park,
      restore, takeover, compat launcher/core, montage) est de l'orchestration
      au-dessus de l'API Docker, le même code Go ; ce qui dépend du processeur
      (netstack gVisor, TUN, checksums, atomiques) est couvert. Les noms
      `flaky`, `res-tko`, `res-agent`, `prev-agent`, `adm-agent` sont déclarés
      côté cluster sans variante arm. À rouvrir si un bug propre à arm
      apparaît, ou avec une deuxième jambe arm (Swarm ou k8s).
- [ ] **Le build d'image reste sur le chemin critique** : un build amd64 lent
      (18 min 30 mesurées une fois, contre 2 min 30) laisse les clusters sans
      rien à tirer. Rien à faire tant que c'est exceptionnel ; `why_no_cluster`
      le nommera.

## 🟢 MCP : `serve` / `unserve` et `explain_failure`

`plug mcp` a `list_profiles`, `doctor`, `env_of`, `resolve_name`, `agent_info`
(`cli/mcp.go`). Manquent `serve` (ouvrir une session depuis l'agent IA) et
`unserve` : une session est un processus qui vit, il faut décider qui le tient
(le serveur MCP ? un daemon ?) et comment l'agent en lit l'état. Et
`explain_failure` : la dernière session et sa raison, ce qui suppose une trace
structurée des fins de session, que plug ne garde pas encore.

## 🟣 UDP par nom (relais de datagrammes) - REPORTÉ (décision 18/07)

Aujourd'hui l'UDP hors DNS vers un nom de plug est jeté avec un log
(« plug tunnels TCP only », `cli/internal/tun/netstack.go`). La motivation
HTTP/3 ne tient pas : par un tunnel TCP, QUIC-over-TCP est pathologique et les
clients h3 retombent en h2. À rouvrir si un cas DNS vers CoreDNS, StatsD ou
syslog réel mord. Plan conservé :

- [ ] **Client** : remplacer le drop par un forwarder UDP (nom par
      `tab.lookup`, cluster par l'attribution TCP), un canal vers l'agent,
      datagrammes préfixés par leur longueur.
- [ ] **Agent** : `plug-agent udp-relay <name> <port>` en session SSH (comme
      `serve-name`), résolution par le resolver du cluster, `net.DialUDP`,
      relais dans les deux sens.
- [ ] **Cycle de vie** : un flux par `(srcport, dst)` et un idle-timeout qui
      ferme canal et relais.
- [ ] **Négociation** : un vieil agent répond « udp-relay not found », on
      retombe sur le drop avec log.
- [ ] **e2e ×3 OS** : DNS sur UDP vers un CoreDNS et un echo UDP ; coverage et
      roadmap mises à jour.

Limite assumée : des datagrammes sur un flux fiable et ordonné. Bon pour DNS,
StatsD, syslog, requête-réponse ; pas pour du média temps réel.

## 🔵 IPv6

- [ ] Fake pool v6 et tunneling des littéraux v6 : les fakes sont IPv4
      (198.18/15), les AAAA répondent NODATA, un paquet v6 égaré est jeté.
      Le service par nom marche déjà.
