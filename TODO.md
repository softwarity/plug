# plug - TODO / plan de travail

_État : 7 août 2026 - **2.10.0 publiée**. L'historique des livraisons vit
désormais dans `RELEASE_NOTES.md` et la roadmap publique dans
`docs/src/app/pages/roadmap.component.ts` ; ce fichier ne garde que ce qui est
**ouvert** et le contexte qui ne tient pas ailleurs._

## 🔴 Ouvert - issu de l'audit du 30/07

- [x] **Durcissement de l'exécution du core mis en cache - FAIT (08-09/08), les trois OS.**
      Conception arrêtée avec François le 08/08, après avoir vérifié le privilège
      réellement détenu par OS : macOS `chown root:wheel` + `chmod u+s` (root
      complet), Windows service SYSTEM, **Linux `setcap cap_net_admin,
      cap_sys_admin,cap_net_bind_service`** - des capacités, ni `cap_dac_override`
      ni `cap_chown`. Linux ne peut donc pas écrire dans un répertoire root, et
      lui ajouter `cap_dac_override` transformerait un grant restreint en grant
      quasi-général : mauvais échange. D'où deux chemins, chacun avec la primitive
      qui existe là :
      · **Linux - livré** : `ensureVersion` rend le *descripteur* vérifié, et le
        launcher exécute `/proc/self/fd/3` (le descripteur passé à l'enfant en
        `ExtraFiles`). Un descripteur est lié à l'inode. Aucun changement de
        privilège, aucun changement de layout, aucune seconde lecture. Test de
        régression avec contre-épreuve dans `coreexec_linux_test.go`.
      · **Windows - sans objet, vérifié** : le launcher n'y est **jamais élevé**
        (« plug elevates per launch… it never runs the child from an inherited
        root euid » - `privdrop_windows.go`, où `guardUserPath` est un no-op pour
        cette raison exacte). Le privilège vit dans le service SYSTEM, qui exécute
        le **launcher installé**, pas un core en cache. Substituer ce core ne
        donne rien que l'attaquant n'ait déjà.
      · **macOS - livré** : `applyPrivDrop` ne s'applique qu'à **ta commande**
        (`main.go:1538`), pas au core - qui tourne donc en `euid 0`. Le magasin est
        sorti de `$HOME` (il ne suffit
        pas que `versions/` appartienne à root - l'utilisateur possède `~/.plug/`
        et peut le renommer, donc toute la chaîne d'ancêtres doit lui échapper),
        vers **`/var/db/plug/versions`** - et pas `/usr/local`, qui appartient
        couramment à l'humain sur un Mac Intel avec Homebrew, ce qui remettrait le
        magasin à portée par son parent. `guardStorePath` le **vérifie** plutôt que
        de s'y fier : chaque composant existant doit appartenir à root et n'être
        écrivable que par lui, sans quoi plug refuse avec la commande exacte à
        lancer. L'installeur le crée `root:wheel 755` avec le sudo qu'il tient
        déjà ; plug le nettoie ensuite lui-même.
      **Migration : aucune, et c'est le bon choix** (tranché par François) - un core
      en cache est *jetable*, re-téléchargé à la demande et vérifié à chaque
      lancement. Il n'y a rien à migrer, seulement à ne pas laisser derrière :
      `prune` connaît l'ancien chemin et le vide. `plug uninstall` retire le
      magasin **avant les binaires** - dans l'autre ordre, seul plug peut effacer
      un répertoire root, et on vient de le supprimer. Les chemins ne peuvent pas être
      littéralement identiques (Linux reste écrivable par l'utilisateur), mais le
      **contrat** peut l'être : un seul accesseur - `plugDir()` renvoie `~/.plug`
      en dur aujourd'hui - que l'installation, `uninstall` et `doctor` lisent.
      **Reste à faire** : `doctor` liste les cores en cache mais ne signale pas un
      reliquat dans l'ancien répertoire - `prune` le nettoie, doctor devrait le
      dire. Et **les notes de version peuvent maintenant raconter l'histoire**,
      les trois OS étant couverts.
- [ ] **Contexte de l'avis** - suivi en PRIVÉ
      (avis de sécurité GitHub, brouillon), pas ici : ce dépôt est public, et
      décrire par le menu un défaut non corrigé revient à en publier le mode
      d'emploi. Ce qui peut se dire sans rien donner : l'**empreinte servie par
      l'agent** sur le canal SSH déjà authentifié, vérifiée avant exécution, est
      **livrée en 2.7.2** ; ce qui reste est le durcissement résiduel, dont la
      forme demande un arbitrage (le coût n'est pas le même selon l'OS). La
      signature de binaires reste souhaitable ensuite, pour une menace
      différente (une source usurpée plutôt qu'une altération locale). Le
      détail, l'impact et la reproduction vivent dans l'avis. À publier dans les
      notes de version **une fois le correctif livré** - c'est là qu'un défaut
      se raconte, pas avant.

## 📋 Contexte

La **2.0.0** est publiée. Elle apporte le **sens retour** : `plug -s
name:cluster-port:local-port` publie ton process dans le cluster sous un nom
(remote-forward sshd, connexion SSH dédiée à la session), le nom est
**provisionné à la volée** (signpost Docker via le socket, service Swarm sur
overlay non-attachable, Service k8s via un rôle RBAC Services-only), et **`-s`
est devenu obligatoire** - une seule forme d'invocation. La **détection de
collision** refuse un nom déjà pris (message précis par backend). Le transport
**s'auto-répare** : keepalive borné qui tue une connexion zombie (sleep / VPN /
proxy Docker Desktop) puis reconnecte et **re-provisionne** le nom. Licence
passée en AGPL-3.0, puis **FSL-1.1-Apache-2.0** (23/07 - protège contre une
gateway commerciale rivale bâtie sur le tunnel, cohérent avec `meerkat`).
Windows est désormais une **jambe e2e complète en CI**
(mesh Tailscale, natif, sans WSL2) - tout l'ancien chantier « verrouiller
Windows » est bouclé.

CI par push, 3 OS × **3 familles de clusters** (Compose, Swarm mono-nœud,
Kubernetes/kind - 9 jambes amd64, plus une jambe Compose **arm64** depuis le
07/08, 6 clusters) : install-depuis-cluster → grille 4 langages × 8 protocoles →
multicluster simultané → outage recovery → env passthrough → `-s` → gateway
callback → collision → takeover → auto-update → compat launcher/core (famille
Compose). **Un seul build d'image** (`sha-<court>`, immuable), consommé tel quel
par les jambes ; il n'est **promu** sous des noms qu'une fois tout vert - branche
→ `:<branche>`, branche par défaut → `+ :latest`, tag `vx.y.z` posé sur HEAD →
`+ :x.y.z, :x.y, :x`. Ce qui est publié est donc, au digest près, ce qui a été
testé. **Un seul build de clients e2e** aussi (`build-clients`), et les clusters
**tirent l'image du registre** au lieu de se la faire livrer en artefact.

---

## 🟡 Ce qui reste hors CI (le banc → CI est bouclé)
- [ ] **Le flake `resilience` - instrumenté le 07/08, pas encore diagnostiqué** : la cellule crashe l'agent par conception ; quand la restauration traîne, elle entraîne `update` et `update_tag` dans sa chute (« 2225 never came back ») - trois cellules rouges pour une cause unique. Ça a coûté la première publication de la 2.10.0. **Ce n'est pas un problème de délai** : `wait_agent` attend déjà 40 × 3 s et la boucle de restore 45 s ; allonger un timeout serait la troisième tentative du même geste. Les deux messages lus ensemble (`connection reset by peer` sur le service, puis le nom absent) désignent l'**agent** qui n'est pas revenu de son redémarrage - et c'est lui qui restaure le service parqué, via boot-gc. D'où `/agent-state` sur le service `chaos` (état du conteneur + 25 dernières lignes, `agent_state()` côté script) : à la prochaine occurrence, on lira au lieu de deviner.
- [ ] **Couverture arm64 : 11 cellules sur 18 - OPTIONNEL, et le pourquoi compte** (07/08). Les noms de cellules dérivent de `$leg` depuis le 07/08 ; restent CINQ noms qui ne peuvent pas suivre parce qu'ils sont **déclarés côté cluster** : `flaky`, `tko`, `res-tko`, `res-agent`, `prev-agent`. Les faire dériver demande d'ajouter les variantes `-linuxarm` dans les trois manifestes (`e2e/compose.cluster.yml`, `swarm.cluster.yml`, `k8s.cluster.yaml`) avec des ports distincts, puis de remplacer les quatre `case "$(uname -s)"` restants (l. 380, 649, 916, 1088 de `e2e-matrix.sh`).
      **Pas fait, délibérément** : ce que ces cinq cellules exercent (park/restore, takeover, compat launcher/core) est de l'orchestration côté agent au-dessus de l'API Docker - le même code Go, indifférent à l'architecture. Ce qui, lui, dépend vraiment du processeur - netstack gVisor, TUN userspace, checksums, atomiques - est déjà couvert sur arm par la matrice de protocoles et le multicluster. La dette initiale (« arm64 publié, jamais exercé ») est payée : l'artefact n'est plus publié à l'aveugle. **À rouvrir si** un bug spécifique arm apparaît, ou le jour où une deuxième jambe arm (swarm ou k8s) est ajoutée - là le coût marginal devient faible et la question change.
- [ ] **Le refus de collision peut désigner le mauvais coupable** (16/08, une occurrence) : quand un nom est encore tenu, plug cherche une session LOCALE (`servedHolder`) et, n'en trouvant pas, écrit « the holder is on another machine or another account ». Vu une fois en CI sur swarm/macOS, entre deux invocations de la MÊME cellule : la marque locale de la session précédente était déjà retirée alors que le bail côté agent ne l'était pas encore. Le message est donc faux dans cette fenêtre - il envoie chercher un collègue pour sa propre session en cours de fermeture. La cellule sait maintenant nommer le refus (elle prenait `tail -1` d'un message multi-ligne et le rapportait comme un échec de relais DNS) ; **ce qui reste ouvert est le message de plug lui-même**, qui gagnerait à distinguer « tenu par quelqu'un d'autre » de « tenu par une session à vous qui se termine ». Une seule occurrence : à revoir si ça se reproduit, la cellule le nommera.
- [x] **`kill-*` avale l'échec d'une annulation - LAISSÉ TEL QUEL, décision du 17/08.** Chaque `gh run cancel` porte un `|| true`, donc un refus laisserait le job vert et un cluster tournerait sans que rien ne le dise. Signalé comme dette ; **François tranche de ne pas y toucher**, et l'arbitrage tient : faire rougir un job de NETTOYAGE pour un hoquet d'annulation échange un silence contre une fausse alerte, et les fausses alertes finissent ignorées comme le reste. La fuite est bornée par deux filets - le TTL du cluster (2100 s) et le `timeout-minutes: 40` du job `serve` - donc un cluster oublié tient un slot de runner 40 min au pire, pas indéfiniment. À rouvrir seulement si une saturation de runners réapparaît (elle a déjà starvé des jobs de cluster par le passé, cf. le commentaire de `concurrency` en tête de ci.yml).
- [ ] **Le correctif du témoin k8s est livré en 2.11.1 SANS note** (17/08) : la release a été lancée avant que la note soit écrite. À rédiger sous `NEXT RELEASE` en précisant qu'elle décrit du déjà-livré - jamais dans la section publiée.
- [ ] **Linux sous VPN d'entreprise : ni prouvé ni écarté** (17/08) : la ligne ci-dessous ne nomme que Windows, macOS ayant son banc GlobalProtect. Linux n'a ni l'un ni l'autre. Le risque y est plus faible - son mécanisme se résume à `resolv.conf`, là où Windows a le NRPT, les métriques d'interface et le filtre loopback - mais autant l'écrire que de le laisser en creux.
- [x] **Cohabitation NRPT - FAIT (17/08)**, `route_windows_test.go` : une règle étrangère (`*.corp.example` → `10.77.88.99`) est écrite, plug installe puis retire la sienne, et la règle étrangère doit être intacte aux deux moments - `setSystemNRPT` nettoie avant d'écrire, c'est le premier endroit où elle pouvait disparaître. La protection existait déjà (`clearSystemNRPT` est indexé sur NOTRE adresse de serveur) mais rien ne la prouvait, et une propriété vraie par accident le reste jusqu'au prochain refactor. Au passage : un échec d'écriture HKLM **échoue** désormais sur un runner (`CI` non vide) au lieu de se sauter en silence - `go test` sans `-v` n'imprime pas les skips, donc un test de sécurité qui ne tourne jamais se lisait comme couvert. Ancien libellé : on teste le round-trip de la propre règle de plug, jamais ce qui se passe quand **une autre règle est déjà là** - celle qu'un client d'entreprise écrit pour router `*.corp.example` vers son résolveur interne. Le mode d'échec est sérieux et silencieux : si plug écrasait cette règle, l'utilisateur perdrait l'accès DNS à tout son intranet sans aucune raison de soupçonner plug. La cellule tiendrait sur un runner GitHub standard, sans VPN : écrire une règle étrangère, monter le datapath, asserter que les deux coexistent, qu'un nom du suffixe étranger passe toujours par son résolveur et un nom de cluster par plug, et qu'à la fermeture plug ne retire **que la sienne**.
- [ ] **Le build d'image reste sur le chemin critique** (17/08) : le tag `sha-<court>-amd64` a sorti le *merge* de la file d'attente, pas la dépendance à l'image elle-même. Un build amd64 lent - 18 min 30 mesurées une fois, contre 2 min 30 d'habitude - laisse encore les clusters sans rien à tirer et perd le run. Rien à corriger tant que ça reste exceptionnel ; à rouvrir si ça se répète, et `why_no_cluster` le nommera.
- [ ] **Sessions longues & charge** : heures, gros transferts, sleep/wake. Piste actée : un workflow « soak » cron hebdo (session tenue 5-6 h, transferts gros volumes, asserts RSS/reconnexions) ; le sleep/wake réel reste un banc local assisté.

## 🟣 UDP par nom (relais de datagrammes) - REPORTÉ (décision 18/07)
La motivation « HTTP/3 » ne tient pas : le relais passerait par le tunnel TCP →
QUIC-over-TCP est pathologique (HOL blocking, tout l'intérêt de QUIC perdu) et
les clients h3 retombent en h2 proprement ; en intra-cluster personne ne parle
h3. Le drop-loud (livré) rend le manque visible et diagnostiqué. À rouvrir si
un cas DNS-vers-CoreDNS / StatsD / syslog réel mord. Plan conservé :
Le tunnel ne porte que du TCP (SSH `direct-tcpip` = stream-only). Le client capte
**déjà** l'UDP (protocole enregistré, `gonet.NewUDPConn` utilisé pour le DNS) et
le droppait en silence hors DNS. Plan :
- [x] **Drop-loud** (18/07) : un flux UDP vers un nom minté loggue « udp `<name>:<port>` dropped - plug tunnels TCP only » (rate-limité 30 s, `udpDropLimiter`) - fin du hang sans diagnostic (flaggé MAJEUR dans `audit.md`).
- [ ] **Client** : remplacer le drop par un forwarder UDP - `tab.lookup` → nom, `df(srcPort)` → cluster (réutilise l'attribution TCP), ouvrir un canal vers l'agent, **framing longueur-préfixée** des datagrammes.
- [ ] **Agent** : sous-commande `plug-agent udp-relay <name> <port>` (invoquée en session SSH comme `serve-name`) → résout via le resolver cluster, `net.DialUDP`, relaie les datagrammes framés dans les deux sens.
- [ ] **Cycle de vie** : flux synthétique par `(srcport, dst)` + **idle-timeout** pour reaper canal + relais (UDP sans-connexion).
- [ ] **Négo de version** : vieil agent → « udp-relay not found » → dégrader proprement en drop-loud (comme serve-name).
- [ ] **e2e ×3 OS** : DNS-sur-UDP vers un CoreDNS + un echo UDP (type StatsD) ; MàJ coverage (ligne UDP `✕` → `!`/`✓`) + roadmap.

Limite assumée : datagrammes → stream fiable/ordonné (HOL blocking, latence). OK
pour DNS / StatsD / syslog / requête-réponse ; pas pour média temps-réel. QUIC =
UDP → porté mais QUIC-over-TCP est pathologique (les clients retombent en TCP de
toute façon).

## 🔵 Transport & intégration (roadmap)
- [ ] **Windows sous VPN d'entreprise** : non prouvé sur un vrai client corpo (macOS OK avec GlobalProtect). Il faut un poste Windows avec un vrai client VPN - la box 192.168.2.17 ferait l'affaire si on y installe le client ; banc ~30 min ensuite. **Ce qui est désormais couvert en CI** (cellule « fake VPN » du selftest, ×3 OS) : une interface de plus portant un résolveur qui connaît un nom que rien d'autre ne connaît, annoncée par la porte que plug lit sur cet OS (2ᵉ adaptateur WinTUN + métrique sur Windows, `resolv.conf` sur Linux, dict DNS du service primaire sur macOS) → plug doit le suivre, résoudre le nom témoin à travers son stub, puis revenir quand le VPN disparaît. **Ce qui reste hors CI** : le split-tunnel (le trafic vers l'IP interne doit emprunter le tunnel), le NRPT / DNS conditionnel Windows, la MTU/fragmentation, et les clients qui interceptent le DNS en `127.0.0.1` (écartés par `pickUpstreams` sur Windows - à confronter à un vrai client corpo).
- [ ] **IPv6** : fake-pool v6 + tunneling des littéraux v6 (fakes IPv4 aujourd'hui ; service par nom déjà OK).
- [ ] **Transport `kubectl exec`** : tunnel via `kubectl exec` sur un pod nu - zéro port exposé, accès gouverné par le kubeconfig RBAC (adoucit le compromis no-auth).
- [ ] **Gateway hôte du tunnel** : la gateway déjà déployée héberge l'endpoint et l'active dynamiquement - son auth devant. Fin de l'agent dédié. C'est **le mécanisme du « plug autorisé ici ou pas »** (dev : oui, prod : non - l'interdiction est une *absence*) et le point 4 des « implications côté plug » de [`meerkat_integration.md`](meerkat_integration.md) - qui fige la conception d'ensemble et les quatre autres chantiers qu'elle induit : auth par clé par dev, attribution nominative des sessions, API d'état (« qui plugge quoi depuis quand »), et plus tard le signpost-proxy L7.

## 🟠 Fuite DNS Docker-Desktop-sur-poste-plugué - RÉSOLUE (18/07)
Docker forwarde les noms inconnus du cluster vers le resolver de la VM → hérite
du DNS du Mac → **resolver plug** quand des sessions tournent → un nom ABSENT du
cluster résolvait vers une fake IP `198.18.x.x` (connection refused au lieu
d'unknown host).
- [x] **Doc** : callout page Swarm - remède `daemon.json "dns": ["1.1.1.1"]` (devenu défense-en-profondeur).
- [x] **Mitigation produit** (18/07) : le **mint est désormais vérifié** - avant de minter un nom nu, le CLI demande à l'agent (verbe `resolve`, cache 5 min/30 s nég) si le nom existe dans un cluster connecté ; absent partout → **NXDOMAIN honnête**. Le helper **filtre les échos 198.18/15** (une réponse dans la plage plug = la boucle poste-plugué, jamais un vrai service) - le fix est donc immunisé contre la boucle qu'il corrige. Vieil agent sans le verbe → mint comme avant (contrat de dégradation). Banc compose ✅ (`no such host` immédiat au lieu de 4 timeouts + refused) ; cellule e2e « dns honesty » ×9 jambes.

## ⚪ Dettes / plus tard
- [x] **Tests unitaires** (18/07) : `answerDNS` strip `.plug` → mint du nom nu (même fake, mapping vers le nom nu) ; round-trip NRPT (`route_windows_test.go`, skip sans élévation - tourne sur la jambe test Windows) ; `DialContext` - un `*ssh.OpenChannelError` ne reconnecte PAS (serveur SSH mock in-process qui rejette les canaux et compte les connexions) ; `ensureVersion` cache-hit + rejet d'un cache tronqué (`launcher_test.go`, HOME isolé).
- [x] **Factoriser `registry_windows`/`registry_darwin`** (18/07) : le commun (95 % identique) vit dans `registry.go` (`darwin || windows`) avec `ClusterHash` ; par-OS il ne reste que `processAlive`. Les tests registry tournent désormais AUSSI sur Windows. `graft_*` : PAS factorisé - divergence structurelle assumée (flock/leader-election macOS vs service unique Windows), plus rien d'accidentellement dupliqué.
- [x] **Version service vs launcher** (19/07) : fermé par `plug update` - le launcher remplacé EST le binaire du service Windows (même `plug.exe`, binPath inchangé), et le service démarre à la demande → la session suivante exécute la nouvelle version, sans UAC. macOS/Linux : re-grant setuid/caps (un sudo) au passage.
- [x] Retirer les directives compose obsolètes (18/07) : `PLUG_HOOK_DEBUG`, `seccomp:unconfined`, `SYS_PTRACE` retirées des 4 clients e2e (aucun usage dans le code ; `apparmor:unconfined` reste - le bind mount-ns en a besoin sur les hosts AppArmor).

---

## ✅ Acquis

### Post-2.10.0 (7 août 2026) - trois familles, un seul bloc
- [x] **Les trois jambes e2e exécutent le MÊME bloc, et on le voit** : elles existent pour prouver que plug se comporte pareil quel que soit le backend qui provisionne le nom - ce qui ne se lit comme une comparaison que si les trois listes sont identiques. Elles avaient dérivé sans que rien ne le signale : le même test s'appelait « park the deployed service », « scale the deployed service to 0 » et « repoint the deployed Service » (trois noms pour une assertion, qui ne différaient que par le mécanisme du backend), l'ordre divergeait, et **deux cellules ne tournaient que sur compose** - `updatenotify` et `compat` - alors que les ressources dont elles ont besoin sont déclarées dans les trois familles (agents N-1 en compose et swarm, nodePorts 32226-32228 mappés par kind). Désormais : **19 cellules, même ordre, mêmes noms**, les noms disant ce qui est ASSERTÉ et jamais comment le backend s'y prend. Swarm et k8s gagnent deux cellules au passage. Et **les trois matrices ont les mêmes trois jambes** : la jambe arm64 vivait dans celle de compose, si bien que le graphe affichait 4 · 3 · 3 et se lisait « compose fait quelque chose que les autres ne font pas » - faux, et impossible à distinguer d'une vraie divergence d'un coup d'œil. Elle a son propre job `e2e-arm64` : elle pose une question d'**architecture**, pas de famille de cluster. `scripts/ci/check-common-block.sh` fait échouer le build à la moindre dérive et **gate la publication** (job `ci-shape`) - sinon un job rouge ne ferait que rougir le run. Ce qu'une seule famille exigerait un jour va **sous** le bloc commun, sous son propre titre ; il n'y a rien aujourd'hui.

### Post-2.10.0 (8 août 2026) - la matrice, et ce que la vitesse a révélé
- [x] **Les quatre langages tournent en même temps** : mesuré d'abord - 187 s pour 32 invocations, soit 5,8 s chacune, dont l'échange de protocole n'est qu'une lichette. Ce qui coûte, c'est **démarrer une session** (SSH, datapath, provisionnement du nom), et on en démarrait 32 à la file. Chaque langage garde ses protocoles en ordre ; ce qui se recouvre, ce sont les quatre runtimes, qui n'ont rien à se dire. Chacun prend un nom et un port à lui (`run-<jambe>-<langage>`, bloc espacé de quatre par jambe) - réclamer `$serve` deux fois au même instant est une collision, sujet d'une autre cellule. **Résultat : 187 s → 21-73 s selon la jambe.** Deux pièges traités : les sorties concurrentes s'entrelaçaient (un fichier par langage, imprimé après la jointure) et le compteur d'échecs mourait avec son sous-shell (recalculé depuis les lignes collectées).
- [x] **`image-build` n'attend plus `test`** : il attendait les tests des trois OS, donc l'image arrivait à t+4 min 16 - et aucun cluster ne répond avant de l'avoir tirée. Mesuré après : **t+3 min 09**. Rien n'est desserré : `image` exige toujours `platforms`, qui exige `test`, donc un test rouge bloque toujours la publication.
- [x] **Un pull figé n'est pas un pull en échec** : la boucle bornait le nombre de tentatives, ce qui suppose que `docker pull` rend la main. Un runner est resté figé plus d'une heure pendant que son jumeau tirait le même digest en secondes - aucune réessai, aucun message, pas de cluster, et les jambes accusaient « cluster never became reachable ». Bornée par l'horloge (15 min) avec 120 s par tentative. Et les jobs de cluster, qui n'avaient **aucun `timeout-minutes`** (six heures par défaut), sont à 40 min : le TTL de secours ne s'arme que dans l'étape de service, jamais atteinte quand ça coince avant.
- [x] **La vitesse a découvert une course qu'elle n'a pas créée** : `wait_cluster` prouve que l'AGENT répond en SSH, jamais que les SERVICES du cluster sont debout. Tant que le setup durait 769 s à construire des clients, tout avait fini de se poser avant la première cellule. À une minute de setup, `client-only` a échoué deux fois dans la journée sur un cluster qui montait encore - et un retry dans la cellule n'y pouvait rien, l'attente nécessaire se comptant en dizaines de secondes. Le setup attend désormais qu'un service réponde par son nom, **une fois pour tout le run**, et imprime combien de temps il a attendu : si ce nombre grandit, c'est le cluster qui ralentit, et ça se lit là plutôt que dans une cellule qui tombe pour une raison qui n'est pas la sienne.

### Post-2.10.0 (7 août 2026) - ce que la CI faisait dix fois
Mesuré sur la jambe la plus lente (swarm/windows, 28 min), en décomposant au lieu
de supposer. Trois postes, tous du travail **répété**, aucun une assertion :
- [x] **Les clients e2e, construits une fois** (`scripts/ci/build-clients.sh`) : 493 s des 769 s de setup partaient dans leur build - go 278 s, node 99 s, java 74 s, python 42 s - et chacune des trois jambes Windows les repayait en entier. Le client Go n'a **aucun cgo** (tous ses drivers sont pur Go), donc un runner Linux cross-compile les quatre cibles ; un jar est du bytecode ; `node_modules` n'est que du JS. Seul python reste par jambe, ses wheels étant compilées. Les jambes ne compilent plus rien : `setup-go` a disparu de leurs étapes, et `echo-local`, que **neuf cellules** rebâtissaient chacune, arrive tout fait. Le bit exécutable ne survivant pas au zip d'un artefact, `take_prebuilt` le rétablit - sans quoi tout échouerait sur « permission denied » d'un fichier bien présent. Le repli (aucun artefact → on construit) reste le chemin d'un run local.
- [x] **Le cache npm ne touchait JAMAIS sur Windows** : il pointait `~/.npm`, qui n'est pas le répertoire de cache de npm là-bas (`%LocalAppData%\npm-cache`) - le log le disait depuis toujours (`Cache not found for npm-Windows-…`, même la clé de repli). 99 s repayées à chaque jambe Windows. Le cache a disparu avec le build qu'il servait.
- [ ] **La grille de protocoles : décomposition TENTÉE puis ANNULÉE le 07/08, et c'est la leçon qui compte.** L'idée était de restreindre swarm et k8s à Go, au motif que l'axe langage teste le CLIENT (quatre résolveurs : le cache de la JVM, c-ares chez Node, la libc chez Python, celui de Go) et l'axe protocole le RÉSEAU de la famille - donc deux axes orthogonaux, les langages se prouvant une fois sur compose. **C'est faux.** Chaque langage n'apporte pas qu'un résolveur : il apporte sa **propre implémentation du protocole** - AMQP, c'est `amqp091-go`, `amqplib`, `pika` et `com.rabbitmq`, quatre bases de code aux framings, heartbeats, pools et tailles d'écriture différents. Ce trafic traverse le tunnel **puis le réseau de la famille**, où une MTU d'overlay VXLAN à 1450 ne répond pas à un driver qui écrit par gros blocs comme à un qui écrit petit. Les deux axes ne sont pas indépendants, et personne n'avait mesuré qu'ils l'étaient. Coût de l'aller-retour : 276 s sur une jambe, non gagnées. `E2E_LANGS` reste comme levier de banc, avec le raisonnement erroné consigné en commentaire pour que personne ne le réinvente.
- [x] **L'image ne voyage plus en artefact** : `docker save` → gzip → upload → download → `docker load`, soit ~2 min pour déplacer des octets que les deux runners pouvaient tirer du registre. Le job `build-agent` ne subsiste que pour le dispatch manuel sans image ; `serve` tire directement. C'était sur le chemin critique de **chaque** jambe - aucune ne démarre avant que son cluster réponde, et l'attente mesurée était de 268 s.

### Post-2.10.0 (7 août 2026) - la dette de l'audit du 30/07, soldée
- [x] **Un seul build d'image, et c'est celle qui est testée** : `compose-for.yml` construisait son propre `softwarity/plug:e2e` (amd64, `VERSION=dev`) pendant que `_docker.yml` en publiait un autre - même Dockerfile, deux artefacts, et l'écart possible est exactement celui qui a tué la 2.7.3 (`apk` et le fetch wintun refaits séparément). Les jambes **tirent** désormais le `sha-<court>` construit en amont ; elles l'**attendent** au lieu de l'exiger (le cluster démarre pendant le build, la référence étant prévisible). Le build par-jambe ne subsiste qu'en dispatch manuel sans image.
- [x] **Le repli du check d'update** : il interrogeait le registre depuis le POSTE, sans recours - derrière un proxy d'entreprise il ne trouvait rien, ne disait rien, et le silence ressemble à « tu es à jour ». Nouveau verbe agent `check-update`, qui réutilise `retarget(img)` - la résolution que fait déjà `self-update` - sans rien appliquer. Un agent trop ancien répond `unknown command`, que le CLI lit déjà comme « pas de réponse » : les clusters anciens gardent le silence d'avant au lieu de casser.
- [x] **Les noms de cellules e2e dérivent de la jambe** : dix-huit `case "$(uname -s)"` produisaient `exposed-linux`, `lease-linux`… - donc deux jambes Linux sur un cluster partagé réclamaient les mêmes noms. Mesuré en ajoutant arm64 : elle a pris `exposed-linux` et **fait tomber la jambe amd64**, pas seulement elle-même. Les noms viennent de `$leg` : la jambe arm passe de 4 à 11 cellules. Restent cinq noms déclarés côté cluster - voir la ligne 🟡, qui explique pourquoi c'est un choix et pas un oubli.
- [x] **Un golden test des remèdes** (`cli/remedies_test.go`) : il lit les **sources**, pas le paquet compilé, donc une machine de n'importe quel OS voit les remèdes des trois - le mauvais conseil qui a coûté une soirée vivait dans `doctor_darwin.go` et `doctor_windows.go`, invisibles depuis un run Linux. Trois règles : toute commande citée est un verbe qui existe, `plug down` n'est prescrit nulle part (il est un **fait** sur la ligne daemon, pas un remède), et un remède contient toujours quelque chose à faire.
- [x] **Le launcher se remplace sur un contenu, plus sur un numéro** : `launcherFollow` comparait `local != remote`, donc un binaire identique entre deux versions était retéléchargé (~9 Mo) et un fichier **setuid root** remplacé pour rien. Le digest servi par l'agent est comparé avant. Un digest indisponible ne fait pas sauter la mise à jour - il fait retomber sur l'ancien critère.
- [x] **`/agent-state` sur le service `chaos`** : état du conteneur + 25 dernières lignes, demandés depuis l'intérieur du cluster (avec `stripDockerFrames()` pour l'en-tête de multiplexage). Posé pour arrêter de deviner sur le flake `resilience`.

### Post-2.3.1 (23 juillet 2026)
- [x] **Relicence AGPL-3.0 → FSL-1.1-Apache-2.0** : l'AGPL autorisait déjà un concurrent à intégrer `plug` dans un produit rival (ex. une gateway commercialisée) - à la seule condition qu'il republie son propre code, une obligation de partage, pas une interdiction. La FSL, elle, interdit directement cet usage concurrent (converge vers Apache-2.0 deux ans après chaque release, comme `meerkat` - cohérence de gamme). Tout le reste (usage libre, interne, intégration dans un produit non-concurrent) reste inchangé. `LICENSE`, badges, section README, `THIRD_PARTY_LICENSES.md`, page About du site mis à jour.

### Post-2.2.0 (19 juillet 2026)
- [x] **`plug update`** : remonte la chaîne de distribution (registre → agent → launcher). Nouveau verbe agent `self-update` : k8s **rolling restart de son propre Deployment** (patch annotation ; le nœud re-pull le tag - RBAC officiel +`deployments get/list/patch`, 403 → remède exact), Swarm **service update forcé, digest retiré** (le manager re-résout le tag), conteneur plain **pull + commande de recréation** (il ne peut pas se recréer lui-même). Puis le **launcher se remplace depuis l'agent** (rename atomique, re-grant setuid/caps ; Windows : le service à la demande prend le nouveau binaire seul - le trou « version service vs launcher » fermé). Jamais de downgrade, jamais sur un build dev. Les sessions `-s` survivent au roll (self-heal). Cellule e2e `update` jambes compose (agents par-jambe), rolling k8s/swarm prouvé au banc M5.
- [x] **`plug doctor`** : diagnostic lecture-seule de toute la chaîne avec remède par constat - binaires (launcher, cores en cache, **la version que le service/daemon exécute réellement** - le trou du bump, désormais détecté et nommé), état système (resolver plug SANS session = état sale ; daemon.json Docker Desktop ; sonde NXDOMAIN live sur le datapath qui tourne), et par profil : agent joignable/version, backend `-s` dynamique (nouveau verbe agent `info`), agent pre-2.2. En fin de rapport interactif : proposition d'**issue GitHub pré-remplie** (le navigateur = login + relecture ; hostnames/IPs rédigés, profils anonymisés - le repo est public). Banc M5 réel ✅ (a trouvé deux vrais problèmes du poste au passage), cellule e2e ×9 jambes.
- [x] **Gate des images de release** : `docker-release.yml` attendait le **vert du run CI du commit taggé** avant de publier les images versionnées - le même contrat que `:latest` (leçon 2.2.0 : image saine partie pendant que la CI échouait sur une cellule cassée). _Le fichier a été supprimé le 07/08 : la CI publie elle-même en fin de course, ce qui rend la gate structurelle au lieu de contractuelle - le job de promotion ne peut pas tourner si les jambes ne sont pas vertes. Effet de bord à connaître : un tag posé sur un commit construit AVANT ne publie plus rien, il faut relancer la CI sur ce commit._
- [x] **Cellule resilience durcie** : agents de crash-test **par jambe** (`res-agent-<leg>`, chaos ciblé par label) - les trois jambes concurrentes ne s'entre-torpillent plus (le teardown perdait son agent quand les jambes s'alignaient) ; le prober témoin passe par l'agent principal, qui ne redémarre plus jamais.

### Post-2.1.0 (18-19 juillet 2026)
- [x] **`-c`/`--client`** (19/07) : consommateur pur (DBeaver, Compass, scripts) - atteint les services par nom, **rien de nommé, aucun port réservé sur l'agent**. Exclusif avec `-s` ; ni l'un ni l'autre → la doc du choix. Garde agent ≥ 2.2, câblage launcher→core comme `-s`, cellule e2e ×9 jambes, banc ✅.
- [x] **CI anti-famine** (19/07) : `concurrency` par branche (un push annule le run précédent) + les serves cluster **suivent leur appelant** (`idle-until-caller-done.sh`, poll 60 s - un run annulé n'orpheline plus ses clusters, le TTL n'est que le filet). Leçon au passage : chemin relatif après un `cd` (exit 127 ×6 clusters) → toujours `$root` absolu, et tester le tail depuis le répertoire piégé.
- [x] **Résilience en CI** : cellule `resilience` sur les jambes compose (cluster B - le A partagé ne voit jamais le blip) : takeover tenu sur `res-tko-<leg>`, le service `chaos` (docker.sock, labels compose scopés, répond AVANT de tirer) **redémarre l'agent en pleine session** → keepalive 5 s détecte, boot-gc restaure, le reconnect re-arme et **re-parque** (~10 s de bout en bout au banc), restore final au ttl. Ferme d'un coup : self-heal (**Windows inclus**), boot-gc, re-park au reconnect, re-arm `-s`. Et `k8s-serve.sh` prouve **kubectl port-forward** comme transport à chaque push.
- [x] **NXDOMAIN honnête** (fix fuite DNS) : voir section 🟠 - vérif pré-mint via le verbe agent `resolve`, filtre anti-écho 198.18/15, cellule « dns honesty » ×9 jambes.
- [x] **Drop-loud UDP** : un flux UDP vers un nom minté loggue la cause au lieu du hang silencieux.
- [x] **Dettes** : registry factorisé (`darwin||windows`, tests sur les 2 OS), 4 tests neufs (strip `.plug`, NRPT, channel-reject-sans-reconnexion, ensureVersion), vestiges compose purgés + banc compose local remis au niveau 2.x (`-s` + sock).

### Post-2.0.0 → 2.1.0 (17-18 juillet 2026)
- [x] **Fix macOS re-assert DNS** (17/07, livré en 2.1.0) : le churn mDNSResponder (locationd → DHCP re-publish ~2/min → configd écrase l'override → re-assert + flush + HUP en boucle → échecs getaddrinfo machine-wide) est corrigé - **re-assert silencieux** quand la config effective pointe encore plug, **débounce** max 1 flush/HUP par 30 s (`flushGate`), lignes du daemon.log **timestampées**.
- [x] **Famille Swarm en CI** (18/07) : `swarm-for.yml`, troisième famille sur le même moule - swarm mono-nœud sur le runner, stack dédiée `e2e/swarm.cluster.yml` (configs Swarm pour rabbitmq/mosquitto, mêmes noms/ports). Prouve ce que seul le banc couvrait : l'agent en **service Swarm sur overlay non-attachable** (défaut stack), `-s` provisionne le nom en **service-signpost** sur cet overlay, takeover scale-0 → **retour au replica count d'origine** (tko à 2 replicas → restore-to-N asserté). Banc M5 sur le Swarm existant (stack throwaway) avant push ; CI verte ×3 OS du premier coup. Piège évité : backreference `\1` en ERE non portable (ugrep la refuse) → awk.
- [x] **Famille k8s en CI** (18/07) : toute la chaîne e2e rejouée contre un cluster **kind** (Kubernetes upstream) - `k8s-for.yml` jumeau de `compose-for.yml` (ex-`cluster.yml`), mêmes noms/ports (`e2e/k8s.cluster.yaml`), agent déployé depuis le **manifeste publié** `deploy/plug-k8s.yaml` (RBAC compris, seule l'image changée) → chaque push bénit le fichier que les users déploient. NodePorts mappés sur le runner (`kind-config.yaml`) → contrat `host:2222`/`:18090` inchangé pour les jambes. **Prouvé au runtime** (banc M5 kind, puis CI ×3 OS) : `-s` crée/détruit le Service via le RBAC réel, takeover repointe le selector (reçu-annotation, restore, **ClusterIP identique** à travers park/restore). Deux leçons au passage : probe exec k8s tourne en **root** → race du cookie Erlang rabbitmq (probe tcp à la place) ; le **keep-alive** d'un caller pré-bascule continue d'atteindre l'ancien pod (pods parqués vivants) → prober sans keep-alive + caveat documenté page Kubernetes.
- [x] **Takeover par défaut** : un nom `-s` tenu par le service déployé est **parqué** pour la session et **restauré** à la fin - conteneurs stoppés (Compose, **e2e CI**), service Swarm scalé 0 → replica count d'origine (**banc M5**), Service k8s re-pointé via annotation-reçu (codé). Reçu de parking sur le signpost → restore par unserve / **boot-gc** (crash agent) / **re-park au reconnect** (banc M5 ✅). Signpost créé AVANT le park (pas de trou DNS - fuite upstream prouvée au banc). D'abord opt-in `--takeover`, puis **défaut** (lancer `plug -s` est déjà l'intention ; flag accepté en no-op) ; un nom tenu par une **autre session** reste refusé ; vieil agent 2.0.x → fallback auto sur son comportement (refus + hint upgrade) ; RBAC k8s +update/patch ; cellules e2e `takeover` + `collision` (inter-sessions) ×3 OS, noms/ports par jambe.

### 2.0.0 (juillet 2026)
- [x] **Sens retour `-s`** : remote-forward sshd, connexion SSH dédiée à la session, port fermé avec la session - e2e ×3 OS
- [x] **Provisionnement dynamique du nom** : signpost Docker (socket), service Swarm sur overlay non-attachable, Service k8s (RBAC Services-only) ; fallback alias statique
- [x] **Gateway callback** : appelant externe → gateway publiée → nom `-s` → sink local du runner (id + chemin complet round-trip) - le cas API-gateway, prouvé depuis l'extérieur
- [x] **`-s` obligatoire** (breaking) : une seule forme d'invocation ; validation du nom côté client (label RFC 1035)
- [x] **Détection de collision** : nom déjà pris refusé, message précis par backend (`docker rm -f` / `docker service rm` / `kubectl delete`) ; scoping réseau du check Swarm ; fix alias-null (`/containers/json` n'a pas les alias → inspect)
- [x] **Self-heal keepalive** : `pingOK` borné, `dropDead` du zombie, `PLUG_KEEPALIVE_SECS` ; re-provision sur reconnexion (`OnRearm`)
- [x] **Version floor** : refus de `-s` contre un agent < 2.0.0 (`coreMajor`)
- [x] **AGPL-3.0** : `LICENSE` racine, badges, section README + contact commercial, `THIRD_PARTY_LICENSES.md`
- [x] **Doc** : About (schéma bidirectionnel + comparatif objectif mirrord/Telepresence), how-it-works bidirectionnel, Getting started allégé
- [x] **CI e2e par famille** (pass/fail visible par étape) ; compat launcher passe `-s`

### Antérieur (1.x)
- [x] **Windows** : service SYSTEM sans-admin + multicluster (PID-at-connect) ; **jambe e2e complète en CI** (mesh, natif, sans WSL2) ; cold-start ~15 s → ~0,8 s
- [x] **Multicluster prouvé en CI sur les 3 OS** (mount-ns Linux · daemon macOS · service Windows), macOS simultané inclus
- [x] Datapath userspace-TUN (tout runtime, Go & gRPC), split-horizon, DNS in-stack sous VPN
- [x] Install depuis le cluster + launcher (versions par cluster) + privilège une seule fois (setcap / setuid / service)
- [x] Grille e2e 8 protocoles × 4 langages, native sur les 3 OS
