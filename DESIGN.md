# shellf — document de design

> Document vivant. Miroir markdown de l'artefact : https://claude.ai/code/artifact/978f0176-274b-444c-940c-8734141e7bd2
> Statut : exploration du meta-langage. Aucun code. Nom `shellf` (de travail, dispo).

Légende statut : ✅ **résolu** · 🟠 **à trancher** · 🔴 **risque / problème dur**

---

## 00 · Thèse produit

### Le pitch (✅ verrouillé)
« **Le shell brut, mais idempotent, prévisualisable et rapide.** »
Chez Ansible/Puppet/Chef, tomber dans le `shell` est la défaite (perte d'idempotence). Or tout le monde y tombe. On construit l'outil **autour** de cette réalité, pas contre elle.

### Les trois paris d'architecture (✅)
- **Agentless, un seul binaire.** L'utilisateur n'a besoin que du binaire + accès SSH. Rien à installer sur les cibles.
- **Agent éphémère qui évalue sur la cible.** Poussé par SSH à la première connexion, il interprète le programme **sur la machine** (une connexion, vitesse quasi-locale). C'est le minion Salt/Puppet, mais sans install permanente. **≠ pyinfra**, qui évalue en local et n'envoie que du shell.
- **Deux plans.** Plan d'**orchestration** (côté contrôle : « sur ces 40 hôtes, dans cet ordre ») + plan d'**exécution** (l'agent, sur la cible). Ne pas les mélanger — c'est le péché de Jinja d'Ansible.

### Philosophie (✅)
**L'utilisateur est responsable, l'outil ne décide pas à sa place** (esprit C / shell, pas SGBD). Cible = power-user. **Contrepartie assumée :** observabilité irréprochable (la liberté sans traçabilité est un lance-flammes).

---

## 01 · Décisions résolues

### Règle de haltage unique (✅)
**Nu = halte sur `err`. Capturé = on te donne le résultat, pas de halte.** Une seule règle, pour `sh` ET pour les instructions. « Capturé » = dans un `if`, un `let`, un `when`.
```
apt-install nginx                   # nu → sucre pour: if (… == err) then stop
if apt-install nginx == ok then …   # capturé → pas de halte, on gère
```

### Le « changed » est un tag, pas un champ (✅)
Pas de `changed_when` séparé. La détection de changement **est** le choix du tag `ok` :
`ok.pkgInstalled` (a changé) vs `ok.pkgAlreadyInstalled` (skip idempotent). Les deux sont `ok`.
```
when apt-install nginx == ok.pkgInstalled -> service-restart nginx
# ne redémarre que si l'install a RÉELLEMENT changé l'état
```

### Comparaison à deux niveaux (✅)
`== ok` matche la **catégorie** (idempotence). `== ok.pkgInstalled` matche le **tag exact** (changement précis). Le type `Result` supporte les deux.

### Dry-run = décisions, pas effets (✅)
On ne prédit pas ce que fait une commande (impossible : lock apt, réseau…). On prédit les **décisions**, en exécutant `pre-check + check + guard` (read-only par contrat) et en **sautant** `apply/post`. Rapport 200 machines : « nginx s'installerait sur 12, déjà présent sur 188 ».
**Contrat qui rend ça possible :** les gardes sont **sans effet de bord**. Sans ça, le dry-run est du flan.

### Trois types de check distincts (✅)
- **Précondition** — « ça *peut* marcher ? » (paquet existe, disque, droits) → échec = **ERREUR**.
- **Garde d'idempotence** — « l'état voulu est-il *déjà* là ? » → succès = **SKIP**.
- **Détection de changement** — « l'apply a-t-il *réellement* changé qqch ? » → rapport / drift (encodé dans le tag `ok`).

### Parallélisme (✅)
- **Inter-hôtes = pilier.** Même plan sur N machines en même temps. Gratuit, gros gain, zéro conflit.
- **Intra-hôte = explicite, à la charge de l'utilisateur.** Bloc `parallel {}` déclaré ; le shell est opaque, l'outil ne peut pas inférer l'absence de conflit.

---

## 02 · Anatomie d'une instruction

Séparabilité = prévisualisabilité : le `check` doit être une **phase distincte** invocable seule, jamais du `if` noyé dans le corps.

| Phase | Rôle |
|-------|------|
| `pre-check` | validation des arguments, pure/locale (nom non vide). Peut tourner côté contrôle, avant tout SSH. |
| `check` | précondition environnementale (le paquet existe dans le dépôt). Nécessite la cible → l'agent. |
| `guard` | état déjà atteint ? Si oui → `return ok.…AlreadyX` (skip). |
| `apply` | l'action réelle. Seule phase avec effet de bord. |
| `post` | après action (nettoyage). 🟠 sémantique à trancher (finally vs on-success). |
| `return` | ensemble des outcomes possibles (union taguée). |

---

## 03 · Primitives du meta-langage

Non pas imaginées — **forcées** par ce qui a cassé en écrivant `apt-install`.

1. **`Result`** — union taguée `catégorie.tag(payload?)`, comparable aux deux niveaux. **Ensemble ouvert** : `err.runtime(shellResult)` obligatoire (le shell échoue de façons non bornées : lock, réseau, disque).
2. **`sh`** — exécution shell structurée → `{ exit, stdout, stderr }`. **Forme args-array** (cf. trou injection) : `sh "apt-get" ["install","-y", pkg]`.
3. **`when cond -> return outcome`** — une phase = suite de `when` gardés ; aucun match → fall-through vers la phase suivante.
4. **`mode` (apply | check)** porté par l'engine — transforme « atteindre `apply` » en `ok.pkgInstalled` (apply) ou `would.pkgInstalled` (check). 🟠 état `would.*` à formaliser.

---

## 04 · Exemple travaillé — `apt-install`

La forme **la plus facile** (ressource binaire : installé / pas installé). Utile pour ancrer, **insuffisant** pour éprouver le langage (cf. §06).

```
instruction apt-install(pkg: str) -> Result {

    pre-check {
        when pkg == "" -> return err.pkgMustNotBeNull
    }

    check {
        let r = sh "apt-cache" ["show", pkg]
        when r.exit != 0 -> return err.pkgInexistant   # ⚠ ment si cache périmé
    }

    guard {
        let r = sh "dpkg" ["-s", pkg]
        when r.exit == 0 -> return ok.pkgAlreadyInstalled
    }

    apply {
        let r = sh "apt-get" ["install", "-y", pkg]
        when r.exit != 0 -> return err.runtime(r)
    }

    post {
        sh "apt-get" ["clean"]
    }

    return ok.pkgInstalled
}
```

**Preuve vivante du « mont Everest des modules » :** même ce cas trivial a un bug — `apt-cache show` ment si le cache est périmé (faux `err.pkgInexistant`). Le langage est le facile ; l'instruction *correcte* est le dur.

---

## 05 · Trous ouverts (à trancher avant d'empiler du code)

### 🟠 L'état `would.*` en mode check
Traverser le guard en mode check n'est ni `ok.pkgInstalled` (mensonge) ni `ok.pkgAlreadyInstalled`. Il manque un 3ᵉ état — `would.pkgInstalled` — synthétisé par l'engine. C'est lui qui alimente le rapport dry-run à 200 machines.

### 🔴 Contrat d'interpolation `sh` — injection
Outil centré shell → l'injection est un risque **de conception**. `sh "apt-get install -y ${pkg}"` avec `pkg = "nginx; rm -rf /"` = catastrophe.
**Proposition en cours (PAS encore validée) :** deux primitives —
- `sh "cmd" [args...]` → exec direct, **pas de shell**, zéro injection (défaut, ~90 % des cas).
- `shell "..."` → passe par `/bin/sh -c`, assume pipes/glob/`&&`, **utilisateur responsable** de l'injection (cohérent avec la philosophie). Le nom `shell` signale le danger.

### 🟠 `post` — `finally` ou on-success ?
Dans l'exemple, guard/err sortent *avant* post → `post` = on-success-only, implicitement. Si le nettoyage doit tourner même sur échec, il faut un bloc `always {}` distinct. **L'ordre des blocs tranche à ta place** — danger de « coder pour affiner ».

### 🔴 Composite non-atomique — échec partiel
Un `apply` multi-étapes (`git-clone; build; install`) n'est pas transactionnel. Si `build` échoue, le repo est cloné : relance → guard « pas installé » → re-clone dans un dossier existant → `err`. **Le guard unique en tête ne rend PAS un composite idempotent.** Ne pas résoudre le rollback maintenant — mais ne pas prétendre que le guard suffit. Piste : chaque sous-instruction porte sa propre garde.

### 🟠 Exécuteur mockable dès le jour 1
« Retour structuré pour tester » n'a de sens que si l'`Executor` shell est une **interface injectable** (l'instruction ne fait pas `run "apt…"` en dur). Contrainte d'architecture **maintenant**, pas plus tard.

---

## 06 · À éprouver ensuite

Un langage ne se prouve pas sur un exemple — il **overfitte**. `apt-install` seul = un DSL d'installeur de paquets. Il faut trois **formes** différentes.

- 🟠 **`file-copy(src, dst)`** — le guard n'est plus « présent/absent » mais « **le contenu correspond-il ?** » → hash/diff. Le mode check doit *differ* sans écrire.
- 🟠 **`service(name, state, enabled)`** — **deux dimensions orthogonales** (tourne ? + activé au boot ?). Un seul `ok/err` ne suffit plus ; teste si `Result` encaisse la multi-dimension.

Si le langage exprime **les trois** sans se tordre, il est éprouvé. Sinon = installeur de paquets déguisé.

---

## 07 · Anti-objectifs & risques stratégiques

### 🔴 Le vrai mur : l'écosystème, pas la technique
Le langage = 5 % du boulot ; la stdlib d'instructions correctes cross-distro + l'écosystème = 95 %. Les alternatives à Ansible meurent de leur **cold-start communautaire**, pas de leur technique. Échappatoire : ne pas *battre* Ansible Galaxy, mais rendre le moment « j'abandonne le module et j'écris du shell » propre et idempotent. Barre abaissée de « des années » à « jour 1 ».

### 🟠 Ne pas coder le parser d'abord
Premier milestone = **moteur + une instruction hardcodée en Go**, poussée par SSH via l'agent éphémère, en mode apply *et* check. Le parser/lexer est fun et tractable — c'est le piège qui évite le vrai risque système.

### 🟠 Ne pas designer les imports / le partage maintenant
« Injecter des var-instructions », « officialiser une instruction » = système de modules + confiance/signature. Prématuré tant que le cœur d'exécution sur une machine n'est pas construit.
