# shellf — historique de la conception

> Digest fidèle de la discussion qui a fait émerger shellf. But : que tout Claude reprenant le projet connaisse **le pourquoi** de chaque décision, pas seulement le quoi. Lire après `DESIGN.md`.
>
> Ton de collaboration explicitement demandé par l'utilisateur : **avis sévère, challenger tout, contredire, zéro flatterie.** Réponses en français.

---

## Le point de départ

L'utilisateur n'aime pas Ansible pour deux raisons : **le YAML** et **l'impossibilité de faire des conditionnels simplement** (le `when:`, la magie Jinja, `register` + `.stdout` — verbeux et fragile). Idée initiale : une alternative sous forme de **vrai langage** où chaque instruction / bloc **renvoie un résultat** exploitable dans des conditionnels.

**Ma réponse sévère :** ça existe déjà (pyinfra en Python, Pulumi dans l'esprit). Surtout, l'angle « plus de YAML, des vrais `if` » rate le vrai problème. Le débat n'est pas *YAML vs langage*, c'est **déclaratif vs impératif**. Le YAML déclaratif d'Ansible est le prix payé pour deux propriétés : **l'idempotence** (`state: present`) et **l'état désiré vs les étapes** (permet dry-run, diff, convergence). Un modèle impératif « run shell, branche sur le résultat » casse ces deux propriétés et te ramène à du Bash.

## Les premières prémisses de l'utilisateur — et leur démolition

L'utilisateur ajoute : **binaire Go sans dépendance**, idempotence must-have, refus d'adopter un outil existant (« non créatif »).

**Ma réponse :**
- « Go sans deps » = détail d'ingénieur, **pas un argument produit**. Personne ne choisit un outil d'infra pour ça. (L'utilisateur l'a reconnu ensuite.)
- « Adopter un outil = non créatif » = piège de l'ego d'ingénieur. Le marché est un cimetière d'outils techniquement supérieurs et morts. La créativité qui compte = résoudre un problème que les autres ratent, pas réécrire.
- **Le vrai moat est ennuyeux** : les ~4000 modules idempotents d'Ansible. Le langage = 2 semaines ; les modules corrects cross-distro = des années. C'est LA question, pas la syntaxe.
- Axes de différenciation proposés : **agent éphémère** (casse le dilemme agentless-fragile vs agent-à-maintenir), **plan/diff fiable**, **DAG de dépendances**, **détection de drift**, et **ne PAS inventer de langage** (embarquer Starlark/CUE).

## La vision se précise

L'utilisateur : n'a besoin que d'un binaire + SSH ; ça doit être **rapide** (Ansible est lent sur les gros scripts) ; **agent éphémère** déployé à la première connexion ; les modules ne sont qu'une **surcouche à du shell** → il veut que tout soit du shell, avec **sortie structurée** (stdout/stderr/exit code) sur laquelle on enchaîne ou on stoppe. Il veut des `if`, des boucles.

**Ma réponse — la contradiction centrale :** « idempotence must-have » vs « run shell et branche » sont **en conflit**. Le modèle run-and-branch est purement impératif : l'idempotence retombe à 100 % sur l'utilisateur qui écrit ses `if` partout = Bash avec plus belle syntaxe. La réconciliation = **la primitive de garde** (le `not_if`/`only_if` de Chef, le `unless`/`onlyif` de Puppet `exec`). L'angle défendable : faire de `exec`-avec-gardes **l'abstraction primaire** au lieu de l'échappatoire honteuse.
Points clés : la **vraie killer feature = évaluer sur la cible** (Ansible est lent parce que le nœud de contrôle pilote tout, très bavard). Sortie structurée = référence **Nushell/PowerShell**. Boucles/`if` = *table stakes*, aucune fierté à en tirer.

## « Est-ce que pyinfra fait ça ? » — Non

**pyinfra évalue le Python EN LOCAL** (phase de build : va chercher des facts, diffe l'état) puis **envoie du shell** par SSH. Le remote ne voit que du shell, aucun interpréteur pyinfra ne tourne dessus. C'est rapide (pas de module Python par tâche comme Ansible) mais reste le modèle control-node-drives-everything.
**L'idée de l'utilisateur est plus agressive** : évaluer le **programme** sur la cible via l'agent = comme un minion Salt/Puppet, **mais éphémère et zéro-install**. C'est la case vide. Prix à payer : le **plan/dry-run devient plus dur** quand l'intelligence est déportée sur la cible.

## Dry-run : « c'est du flan » — à moitié vrai

L'utilisateur : impossible de prédire ce que fait une commande (ex. lock sur la DB apt). Donc dry-run inutile.
**Ma réponse :** il confond deux choses. Prédire les **effets** = impossible (le lock apt, personne ne le prédit). Prédire les **décisions** = possible et c'est ça que veut l'ops : « sur combien de mes 200 machines nginx va-t-il s'installer ? », calculé en exécutant **seulement les gardes read-only**, sans lancer les actions. **Conséquence de design :** les gardes doivent être **sans effet de bord** — contrat qui rend le dry-run honnête.

## Parallélisme : le bon étage

L'utilisateur veut paralléliser des commandes sans conflit ; **l'utilisateur décide**, l'outil ne décide pas à sa place.
**Ma réponse :** l'outil ne **peut pas** inférer l'absence de conflit (le shell est opaque). Le parallélisme **intra-hôte** est un piège faible valeur (deux `apt` sont sérialisés par le lock dpkg). Le gain réel est **inter-hôtes** (gratuit, indépendant, énorme). L'utilisateur a maintenu sa philosophie « utilisateur responsable » ; validé, avec la contrepartie **observabilité irréprochable**. Son exemple valide : télécharger un tarball *pendant* qu'on clone/build un repo = deux I/O indépendantes, `parallel {}` explicite.

## Instructions réutilisables & cross-distro

L'utilisateur : des **var-instructions** (blocs paramétrés réutilisables), ex. `pkg-install` qui appelle `what-os` et dispatche vers `apt-install` (debian) / `dnf-install` (rhel).
**Ma réponse :** excellent — c'est ainsi qu'on **bootstrappe les modules depuis des primitives** et qu'on reconstruit le cross-distro par dispatch sur les facts. **Mais** renommer « module » en « instruction réutilisable » **ne réduit pas le travail** : on recrée Ansible Galaxy / Chef Supermarket (langage + stdlib + écosystème). Le langage = 5 %, l'écosystème = 95 %.
**Contre-argument fort de l'utilisateur (sa meilleure idée) :** les modules Galaxy ne sont pas magiques, souvent ils ne marchent pas dans un contexte donné → on les abandonne et on écrit du shell. **Reformulé en thèse produit :** le shell n'est pas l'échappatoire honteuse, c'est le citoyen de première classe — et il reste idempotent/prévisualisable même en shell brut. Ça **abaisse le cold-start** : pas besoin de battre Galaxy, juste rendre le fallback-shell propre.

## La décision d'architecture n°1 : séparabilité

L'utilisateur esquisse une instruction avec des `if` dans le corps (`if !apt-pkg-exists return err; if installed skip; else install`).
**Ma réponse — le piège invisible :** en entrelaçant check et action dans un même corps, **on ne peut plus exécuter le check sans exécuter l'action** → la prévisualisation est morte. Règle : **le `check` doit être structurellement séparable de l'`apply`** (comme Chef `not_if`, Terraform `plan`/`apply`, React render/commit). Séparabilité = prévisualisabilité. Un seul mécanisme donne alors idempotence + dry-run + détection de changement.
Aussi : distinguer **trois types de check** (précondition→ERREUR, garde→SKIP, changed→report), pas un « verify » plat.

## Quatre décisions sémantiques que « le code ne raffinera pas »

1. **Halte vs testable** — contradiction entre « une err stoppe » et `if apt-install == ok`. **Résolu par l'utilisateur :** nu = halte sur err ; capturé (`if`/`let`/`when`) = pas de halte. Appel nu = sucre pour `if (…==err) then stop`.
2. **Ensemble d'outcomes ouvert** — le lock dpkg prouve qu'on ne peut pas tout énumérer → `err.runtime(shellResult)` obligatoire.
3. **`post` = finally ou on-success ?** — l'ordre des blocs tranche implicitement ; à décider explicitement. (🟠 ouvert)
4. **Composite non-atomique** — un `apply` multi-étapes n'est pas transactionnel ; le guard unique en tête ne rend pas idempotent (git-clone qui échoue à mi-course). (🔴 ouvert)

L'utilisateur a acté : **« coder pour affiner c'est du bullshit »**, on cadre le meta-langage d'abord. Il ne sait pas encore écrire la signature exacte d'une instruction — normal, elle découle des réponses aux questions ci-dessus.

## `apt-install` v0 — deux réussites, trois trous

(Voir le code dans `DESIGN.md §04`.)
**Réussites à garder :** (1) **une seule règle de haltage** pour `sh` et instructions ; (2) le **« changed » est un tag** (`ok.pkgInstalled` vs `ok.pkgAlreadyInstalled`), pas un champ `changed_when` séparé → comparaison à deux niveaux (`== ok` / `== ok.tag`).
**Trous :** (1) l'état **`would.*`** manquant en mode check ; (2) **injection** (interpolation shell) ; (3) **`post`** finally-vs-success. Plus la preuve vivante du mont Everest : `apt-cache show` ment si le cache est périmé.
**Avertissement :** un langage prouvé sur `apt-install` seul **overfitte** (forme binaire installé/pas-installé). Il faut aussi **`file-copy`** (garde par contenu = hash/diff) et **`service`** (deux dimensions orthogonales).

## Le nom

`shelld` (mon 1er jet) rejeté : le suffixe `-d` = daemon (sshd, systemd), or l'outil est **agentless/éphémère** → le nom ment sur la thèse. L'utilisateur veut **`shell` dans le nom** (affiche la vision). Vérifiés **pris** : `shellward` (sécurité IA), `shellcast`, `shellflow`, `seashell`. **`shellf` = libre.** L'étagère (« shelf ») ne colle pas vraiment au concept (stockage statique vs exécution dynamique) — le vrai cadrage honnête : `shell` (déjà chargé de sens) + `f` pour l'unicité. Bonus : jeu de mots avec **Chef** (qui range ses *cookbooks* sur une étagère) → easter egg README, pas axe d'identité (Chef est en déclin). **Nom de travail verrouillé : `shellf`.**

## En cours au moment d'écrire ce doc

Décision du **contrat d'interpolation `sh`** (le trou 🔴 injection). Proposition non encore validée par l'utilisateur :
- `sh "cmd" [args...]` → exec direct, **pas de shell**, zéro injection (défaut).
- `shell "..."` → `/bin/sh -c`, pipes/glob/`&&`, **utilisateur responsable** (le nom signale le danger).

Prochaine étape après validation : écrire **`file-copy`** pour faire hurler le langage sur une autre forme de garde.
