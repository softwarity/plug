# plug - TODO / plan de travail

_État : 5 septembre 2026 - **2.14.0 publiée**. L'historique des livraisons vit
dans `RELEASE_NOTES.md` et la roadmap publique dans
`docs/src/app/pages/roadmap.component.ts` ; ce fichier ne garde que ce qui est
**ouvert** et le contexte qui ne tient pas ailleurs. Les 55 items terminés et la
section « Acquis » ont été retirés le 05/09 : ils racontaient une seconde fois, et
moins bien, ce que les notes de version tiennent à jour._

## 📋 Contexte CI

CI par push, 3 OS × **3 familles de clusters** (Compose, Swarm mono-nœud,
Kubernetes/kind - 9 jambes amd64, plus une jambe Compose **arm64** depuis le
07/08, 6 clusters). Les trois familles exécutent le **même bloc de cellules** (22
aujourd'hui) et `scripts/ci/check-common-block.sh` fait échouer le build à la
moindre dérive : c'est lui la liste à jour, pas ce fichier. **Un seul build
d'image** (`sha-<court>`, immuable), consommé tel quel par les jambes ; il n'est
**promu** sous des noms qu'une fois tout vert - branche → `:<branche>`, branche
par défaut → `+ :latest`, tag `vx.y.z` posé sur HEAD → `+ :x.y.z, :x.y, :x`. Ce
qui est publié est donc, au digest près, ce qui a été testé. **Un seul build de
clients e2e** aussi (`build-clients`), et les clusters **tirent l'image du
registre** au lieu de se la faire livrer en artefact.

## 🔴 Sécurité - suivi en privé

- [ ] **Contexte de l'avis** - suivi en PRIVÉ
      (avis de sécurité GitHub, brouillon), pas ici : ce dépôt est public, et
      décrire par le menu un défaut non corrigé revient à en publier le mode
      d'emploi. Ce qui peut se dire sans rien donner : l'**empreinte servie par
      l'agent** sur le canal SSH déjà authentifié, vérifiée avant exécution, est
      **livrée en 2.7.2** ; ce qui reste est le durcissement résiduel, dont la
      forme demande un arbitrage (le coût n'est pas le même selon l'OS). La
      signature de binaires, elle, **est livrée** : `cli/release_sig.go` porte
      l'ancre compilée, `cli/cmd/plug-sign` produit les signatures et la clé
      privée ne quitte pas le workflow de release (ligne corrigée le 06/09, elle
      la disait encore à faire). Le
      détail, l'impact et la reproduction vivent dans l'avis. À publier dans les
      notes de version **une fois le correctif livré** - c'est là qu'un défaut
      se raconte, pas avant.

## 🟡 Ce qui reste hors CI (le banc → CI est bouclé)
- [ ] **Le flake `resilience` - instrumenté le 07/08, pas encore diagnostiqué** : la cellule crashe l'agent par conception ; quand la restauration traîne, elle entraîne `update` et `update_tag` dans sa chute (« 2225 never came back ») - trois cellules rouges pour une cause unique. Ça a coûté la première publication de la 2.10.0. **Ce n'est pas un problème de délai** : `wait_agent` attend déjà 40 × 3 s et la boucle de restore 45 s ; allonger un timeout serait la troisième tentative du même geste. Les deux messages lus ensemble (`connection reset by peer` sur le service, puis le nom absent) désignent l'**agent** qui n'est pas revenu de son redémarrage - et c'est lui qui restaure le service parqué, via boot-gc. D'où `/agent-state` sur le service `chaos` (état du conteneur + 25 dernières lignes, `agent_state()` côté script) : à la prochaine occurrence, on lira au lieu de deviner.
- [ ] **Couverture arm64 : 11 cellules sur 22 - OPTIONNEL, et le pourquoi compte** (07/08). Les noms de cellules dérivent de `$leg` depuis le 07/08 ; restent CINQ noms qui ne peuvent pas suivre parce qu'ils sont **déclarés côté cluster** : `flaky`, `tko`, `res-tko`, `res-agent`, `prev-agent`. Les faire dériver demande d'ajouter les variantes `-linuxarm` dans les trois manifestes (`e2e/compose.cluster.yml`, `swarm.cluster.yml`, `k8s.cluster.yaml`) avec des ports distincts, puis de remplacer les `case "$(uname -s)"` qui restent dans `e2e-matrix.sh` (un grep les donne ; pas de numéros de ligne ici, ils bougent à chaque cellule ajoutée).
      **Pas fait, délibérément** : ce que ces cinq cellules exercent (park/restore, takeover, compat launcher/core) est de l'orchestration côté agent au-dessus de l'API Docker - le même code Go, indifférent à l'architecture. Ce qui, lui, dépend vraiment du processeur - netstack gVisor, TUN userspace, checksums, atomiques - est déjà couvert sur arm par la matrice de protocoles et le multicluster. La dette initiale (« arm64 publié, jamais exercé ») est payée : l'artefact n'est plus publié à l'aveugle. **À rouvrir si** un bug spécifique arm apparaît, ou le jour où une deuxième jambe arm (swarm ou k8s) est ajoutée - là le coût marginal devient faible et la question change.
- [ ] **Le refus de collision peut désigner le mauvais coupable** (16/08, une occurrence) : quand un nom est encore tenu, plug cherche une session LOCALE (`servedHolder`) et, n'en trouvant pas, écrit « the holder is on another machine or another account ». Vu une fois en CI sur swarm/macOS, entre deux invocations de la MÊME cellule : la marque locale de la session précédente était déjà retirée alors que le bail côté agent ne l'était pas encore. Le message est donc faux dans cette fenêtre - il envoie chercher un collègue pour sa propre session en cours de fermeture. La cellule sait maintenant nommer le refus (elle prenait `tail -1` d'un message multi-ligne et le rapportait comme un échec de relais DNS) ; **ce qui reste ouvert est le message de plug lui-même**, qui gagnerait à distinguer « tenu par quelqu'un d'autre » de « tenu par une session à vous qui se termine ». Une seule occurrence : à revoir si ça se reproduit, la cellule le nommera.
- [ ] **Windows sous VPN d'entreprise** : non prouvé sur un vrai client corpo (macOS OK avec GlobalProtect). Il faut un poste Windows avec un vrai client VPN - la box 192.168.2.17 ferait l'affaire si on y installe le client ; banc ~30 min ensuite. **Ce qui est désormais couvert en CI** (cellule « fake VPN » du selftest, ×3 OS) : une interface de plus portant un résolveur qui connaît un nom que rien d'autre ne connaît, annoncée par la porte que plug lit sur cet OS (2ᵉ adaptateur WinTUN + métrique sur Windows, `resolv.conf` sur Linux, dict DNS du service primaire sur macOS) → plug doit le suivre, résoudre le nom témoin à travers son stub, puis revenir quand le VPN disparaît. **Ce qui reste hors CI** : le split-tunnel (le trafic vers l'IP interne doit emprunter le tunnel), le NRPT / DNS conditionnel Windows, la MTU/fragmentation, et les clients qui interceptent le DNS en `127.0.0.1` (écartés par `pickUpstreams` sur Windows - à confronter à un vrai client corpo).
- [ ] **Linux sous VPN d'entreprise : ni prouvé ni écarté** (17/08) : la ligne ci-dessus ne nomme que Windows, macOS ayant son banc GlobalProtect. Linux n'a ni l'un ni l'autre. Le risque y est plus faible - son mécanisme se résume à `resolv.conf`, là où Windows a le NRPT, les métriques d'interface et le filtre loopback - mais autant l'écrire que de le laisser en creux.
- [ ] **Le build d'image reste sur le chemin critique** (17/08) : le tag `sha-<court>-amd64` a sorti le *merge* de la file d'attente, pas la dépendance à l'image elle-même. Un build amd64 lent - 18 min 30 mesurées une fois, contre 2 min 30 d'habitude - laisse encore les clusters sans rien à tirer et perd le run. Rien à corriger tant que ça reste exceptionnel ; à rouvrir si ça se répète, et `why_no_cluster` le nommera.
- [ ] **Une session qui démarre sans jamais lancer la commande - vu UNE fois (06/09), inexpliqué** : au tout premier run du soak, `plug --host <ip> --port 22 -c sh -c '<boucle curl>'` a vécu ses 12 minutes sans exécuter la commande une seule fois - `traffic.log` vide, ni code HTTP ni `ERR`, donc la boucle n'a pas tourné. Le processus était bien vivant (RSS 16 Mo, 6 threads, contre 30 Mo et 13 threads pour une session saine mesurée juste après) : la signature d'un launcher qui n'a pas monté son datapath. Écartés par lecture du code : le parsing (le premier argument non-option termine les options, le `-c` de `sh -c` voyage avec la commande) et la bufferisation (`child.Stdout` est hérité, pas relayé). Le run suivant, même commande, même dispositif, a tourné normalement : **intermittent**. Le soak échoue désormais à 20 s si aucun tour n'est sorti et imprime le stderr de plug, qui est aussi collecté en artefact - à la prochaine occurrence on lira au lieu de deviner.
- [ ] **Sessions longues & charge - le soak couvre la DURÉE, pas la charge** (06/09) : `scripts/ci/soak.sh` + `.github/workflows/soak.yml` tiennent une session 4 h chaque lundi contre l'image PUBLIÉE, avec un trafic léger (une requête toutes les 2 s, qui re-résout le nom à chaque tour), et assèrent une tendance sur RSS / descripteurs / threads de tout l'arbre plutôt qu'un seuil. 4 h et non 6 : un job GitHub plafonne à 6 h et un job qui meurt sur son timeout perd son log, donc ses chiffres. **Restent ouverts** : les gros transferts (le soak ne pousse que des requêtes courtes, une fuite proportionnelle au VOLUME lui échapperait) et le sleep/wake, qui ne se teste pas sur un runner et reste un banc local assisté.

## 🟣 UDP par nom (relais de datagrammes) - REPORTÉ (décision 18/07)
La motivation « HTTP/3 » ne tient pas : le relais passerait par le tunnel TCP →
QUIC-over-TCP est pathologique (HOL blocking, tout l'intérêt de QUIC perdu) et
les clients h3 retombent en h2 proprement ; en intra-cluster personne ne parle
h3. Le drop-loud (livré) rend le manque visible et diagnostiqué. À rouvrir si
un cas DNS-vers-CoreDNS / StatsD / syslog réel mord. Plan conservé :
Le tunnel ne porte que du TCP (SSH `direct-tcpip` = stream-only). Le client capte
**déjà** l'UDP (protocole enregistré, `gonet.NewUDPConn` utilisé pour le DNS) et
le droppait en silence hors DNS. Plan :
- [ ] **Client** : remplacer le drop par un forwarder UDP - `tab.lookup` → nom, `df(srcPort)` → cluster (réutilise l'attribution TCP), ouvrir un canal vers l'agent, **framing longueur-préfixée** des datagrammes.
- [ ] **Agent** : sous-commande `plug-agent udp-relay <name> <port>` (invoquée en session SSH comme `serve-name`) → résout via le resolver cluster, `net.DialUDP`, relaie les datagrammes framés dans les deux sens.
- [ ] **Cycle de vie** : flux synthétique par `(srcport, dst)` + **idle-timeout** pour reaper canal + relais (UDP sans-connexion).
- [ ] **Négo de version** : vieil agent → « udp-relay not found » → dégrader proprement en drop-loud (comme serve-name).
- [ ] **e2e ×3 OS** : DNS-sur-UDP vers un CoreDNS + un echo UDP (type StatsD) ; MàJ coverage (ligne UDP `✕` → `!`/`✓`) + roadmap.

Limite assumée : datagrammes → stream fiable/ordonné (HOL blocking, latence). OK
pour DNS / StatsD / syslog / requête-réponse ; pas pour média temps-réel. QUIC =
UDP → porté mais QUIC-over-TCP est pathologique (les clients retombent en TCP de
toute façon).

## 🔵 Transport & intégration (statut public : `roadmap.component.ts`)
- [ ] **IPv6** : fake-pool v6 + tunneling des littéraux v6 (fakes IPv4 aujourd'hui ; service par nom déjà OK).
- [ ] **Gateway hôte du tunnel** : la gateway déjà déployée héberge l'endpoint et l'active dynamiquement - son auth devant. Fin de l'agent dédié. C'est **le mécanisme du « plug autorisé ici ou pas »** (dev : oui, prod : non - l'interdiction est une *absence*) et le point 4 des « implications côté plug » de la note de conception Meerkat (retirée du dépôt le 05/09, sa conception ayant vieilli) - qui figeait la conception d'ensemble et les quatre autres chantiers qu'elle induit : auth par clé par dev, attribution nominative des sessions, API d'état (« qui plugge quoi depuis quand »), et plus tard le signpost-proxy L7.

## ⚪ Dettes / plus tard
- [ ] **`doctor` ne signale pas un reliquat de l'ancien magasin de cores** : depuis
      que le magasin est sorti de `$HOME` (macOS `/var/db/plug/versions`), `prune`
      connaît l'ancien chemin et le vide, mais `doctor` liste les cores en cache
      sans dire qu'il en reste dans l'ancien répertoire. Il devrait le dire.
