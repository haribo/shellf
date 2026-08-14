# shellf — design

> **Ce que fait ce document :** il dit *pourquoi shellf est bâti ainsi* — la thèse produit,
> les paris d'architecture, ce qui reste ouvert. Il ne décrit pas le langage : ça, c'est
> [`language.md`](language.md). L'historique des décisions, avec leurs alternatives
> rejetées, vit dans [`adr/`](adr/) — pas ici.
>
> Statut : langage **implémenté** (interpréteur, transport SSH, agent résident, stdlib
> embarquée). Nom `shellf` (de travail, dispo).

Légende : 🟠 **à trancher** · 🔴 **risque / problème dur**

---

## 00 · Thèse produit

### Le pitch

« **Le shell brut, mais idempotent, prévisualisable et rapide.** »

Chez Ansible/Puppet/Chef, tomber dans le `shell` est la défaite (perte d'idempotence). Or
tout le monde y tombe. On construit l'outil **autour** de cette réalité, pas contre elle.

### Les trois paris d'architecture

- **Agentless, un seul binaire.** L'utilisateur n'a besoin que du binaire + accès SSH.
  Rien à installer sur les cibles.
- **Agent résident auto-nettoyé qui évalue sur la cible.** Poussé par SSH, mis en cache par
  hash et laissé **résident** entre les jobs ; il s'auto-efface après un TTL d'inactivité
  (rien ne survit à un reboot) — pas d'install permanente durable
  ([ADR-0005](adr/0005-agent-lifecycle.md)). Il interprète le programme **sur la machine**
  (une connexion, vitesse quasi-locale). C'est le minion Salt/Puppet. **≠ pyinfra**, qui
  évalue en local et n'envoie que du shell.
- **Deux plans.** Plan d'**orchestration** (côté contrôle : « sur ces 40 hôtes, dans cet
  ordre ») + plan d'**exécution** (l'agent, sur la cible). Ne pas les mélanger — c'est le
  péché de Jinja d'Ansible.

### Philosophie

**L'utilisateur est responsable, l'outil ne décide pas à sa place** (esprit C / shell, pas
SGBD). Cible = power-user. **Contrepartie assumée :** observabilité irréprochable (la
liberté sans traçabilité est un lance-flammes).

---

## 01 · Ce que l'architecture doit garantir

### Dry-run = décisions, pas effets

On ne prédit pas ce que fait une commande (impossible : lock apt, réseau…). On prédit les
**décisions** : les phases de lecture s'exécutent, la phase d'action est sautée. Rapport
200 machines : « nginx s'installerait sur 12, déjà présent sur 188 ».

**Le contrat qui rend ça possible :** les phases de lecture sont **sans effet de bord**.
Sans ça, le dry-run est du flan. Quelles phases, dans quel mode :
[`language.md`](language.md), [ADR-0035](adr/0035-phases-and-modes.md).

C'est un contrat, donc il se vérifie : le moteur ne doit jamais appeler la phase d'action
en dry-run ni en `status`. Il l'a fait pendant des mois sans que rien ne le voie
(#338) — l'invariant était écrit dans un commentaire, pas dans un test.

### Trois questions distinctes, trois phases

- **Précondition** — « ça *peut* marcher ? » (paquet existe, disque, droits) → échec =
  **erreur**, avant toute action.
- **Observation d'état** ([ADR-0013](adr/0013-observe-state-contract.md)) — l'état
  *courant*, comparé aux arguments voulus → convergé = **skip**. Au lieu d'un booléen
  écrit à la main, l'instruction renvoie son état et le moteur en dérive le skip *et* le
  rapport `status`.
- **Détection de changement** — « l'action a-t-elle *réellement* changé quelque chose ? »
  → alimente le rapport et le chaînage (`if x.changed { … }`).

Les confondre, c'est perdre soit l'idempotence, soit la prévisualisation.

### Parallélisme

- **Inter-hôtes = pilier.** Même plan sur N machines en même temps. Gratuit, gros gain,
  zéro conflit.
- **Intra-hôte = explicite, à la charge de l'utilisateur.** Bloc `parallel { }` déclaré ;
  le shell est opaque, l'outil ne peut pas inférer l'absence de conflit.

---

## 02 · Trous ouverts

### 🔴 Composite non-atomique — échec partiel

Une action multi-étapes (`git-clone; build; install`) n'est pas transactionnelle. Si
`build` échoue, le dépôt est cloné : relance → l'observation dit « pas installé » →
re-clone dans un dossier existant → erreur. **Une observation unique en tête ne rend PAS
un composite idempotent.** Le rollback n'est pas résolu, et il ne faut pas prétendre que
l'observation suffit. Piste : chaque sous-instruction porte la sienne — ce que la
délégation ([ADR-0037](adr/0037-explicit-verdict.md)) rend possible pour un appel, pas
encore pour une suite.

### 🔴 Le vrai mur : l'écosystème, pas la technique

Le langage = 5 % du boulot ; la stdlib d'instructions correctes cross-distro +
l'écosystème = 95 %. Les alternatives à Ansible meurent de leur **cold-start
communautaire**, pas de leur technique. Échappatoire : ne pas *battre* Ansible Galaxy,
mais rendre le moment « j'abandonne le module et j'écris du shell » propre et idempotent.
Barre abaissée de « des années » à « jour 1 ».

### 🟠 Éprouver le langage sur des formes différentes

Un langage ne se prouve pas sur un exemple — il **overfitte**. `file.copy` (l'observation
n'est plus « présent/absent » mais « le contenu correspond-il ? ») et `service.ensure`
(deux dimensions orthogonales : tourne ? + activé au boot ?) existent aujourd'hui et n'ont
pas tordu le modèle. La question reste ouverte pour ce qui viendra : une forme qui ne
rentre pas est un signal, pas un détail d'implémentation.
