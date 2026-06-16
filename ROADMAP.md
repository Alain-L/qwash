# Roadmap qwash

> Issue de la campagne de durcissement de juin 2026 (28 commits, validée par 6 audits
> indépendants à œil neuf) et de la décision de pivot : faire évoluer qwash d'un outil
> de bloat vers un **outil d'audit / introspection PostgreSQL + routines de maintenance**,
> dont le bloat devient un sous-domaine. Pensé pour s'utiliser en binôme avec **quellog**
> (qwash = instance vivante / catalogue ; quellog = logs) avec un vocabulaire CLI aligné.

---

## Vue d'ensemble

| Version | Thème | Statut |
|---|---|---|
| **0.5.0** | Hardening / fiabilité (bloat, connexion, sûreté, estimations, industrialisation) | ✅ fait sur `dev`, à tagger |
| **0.6.0** | **Fondation audit** : refonte CLI + reporting des index | 🎯 prochaine |
| 0.7.0+ | Sections d'audit additionnelles (config, sécurité, réplication, verrous…) | à venir, une à la fois |

Format des items : `- [ ] **Titre** · Px · ~effort` + pitch + Action + Synergies.
**P0** = bloquant / quick win · **P1** = haute valeur · **P2** = nice to have.

---

## Principes directeurs (issus de la session de pivot)

1. **Safe by default.** La commande nue = audit **read-only** (sûr sur prod client). La mutation exige un **verbe explicite**.
2. **Crédibilité des chiffres.** Un outil d'audit qu'on sort en clientèle doit pouvoir défendre chaque nombre. Signaler les angles morts (stats périmées, biais réplica, etc.) plutôt que dumper — c'est ce qui le distingue d'un `check_postgres` copié-collé.
3. **Alignement quellog.** Même modèle CLI (commande + flags-sections + format partagé), mêmes noms de flags là où le concept est commun (`--maintenance`, `--locks`, `--connections`, `-J/--json`, `-Y/--yaml`, `--md`) → mémoire musculaire et rapports homogènes.
4. **Profondeur > largeur.** Ajouter les sous-domaines un à un, chacun prouvé (tests + matrice CI), plutôt qu'une dizaine de checks superficiels.
5. **La mutation passe toujours par le framework de préflight/sûreté** déjà en place (propriété, `session_replication_role`, REPLICA IDENTITY, annulation, exit codes).

---

## 0.6.0 — Fondation audit

### 6.1 Refonte CLI (l'enabler)

- [ ] **Passer en modèle « commande + flags-sections », aligné quellog** · P0 · ~gros
  - Aujourd'hui : flags plats mutuellement exclusifs (`--estimate`/`--debloat`), `cmd/root.go` monolithe (~1100 lignes, ~47 globals). Ne scale pas pour un généraliste.
  - Cible :
    ```
    qwash [conn] [sections] [format]        ← commande NUE = audit (read-only, toutes sections par défaut)
      sections : --bloat --indexes --maintenance --storage
                 (à venir : --config --security --replication --connections --locks --activity)
      format   : -J/--json  -Y/--yaml  --md          (partagés avec quellog ; -J déjà présent)
    qwash audit …                           ← synonyme explicite de la commande nue (découvrabilité/scripts)
    qwash repack | vacuum | reindex         ← MUTATION : verbes explicites, opt-in
    ```
  - `qwash` sans argument → audit complet sur l'instance résolue (`PG*`/`.pgpass`), `--help` toujours dispo.
  - **Action** : restructurer `cmd/` (Cobra : root avec `Run` par défaut + sous-commandes de mutation). C'est le refacto `cmd/root.go` (ex-G6 du hardening) — désormais justifié.
  - **Non-breaking obligatoire** : les anciens flags restent fonctionnels en **alias dépréciés un cycle** (`--estimate` → `qwash audit --bloat`, `--debloat` → `qwash repack`), avec un `[DEPRECATED] use '…'` sur stderr. Objectif : **les 91 tests d'intégration existants passent inchangés**, puis on ajoute les tests de la nouvelle forme.
  - **Synergies** : débloque toutes les sections futures (« juste ajouter une section »).

- [ ] **`qwash repack` : nouveau nom de la mutation de bloat** · P0 · ~petit
  - « repack » = terme standard DBA (pg_repack) pour « réorganiser une table et récupérer l'espace » ; plus parlant que « debloat ». Modèle mental : *l'audit montre, le verbe corrige* (`audit --bloat` → `repack`, `--maintenance` → `vacuum`/`analyze`, `--indexes` → `reindex`).
  - **À documenter honnêtement** : `qwash repack` utilise **sa propre méthode incrémentale** (compaction de pages par UPDATE, in-place, sans extension, sans 2× l'espace) — proche de **pgcompacttable**, et **différente** de `pg_repack` (réécriture + swap) comme de `VACUUM FULL` / `REPACK` core.
  - **Note PG19 (vérifié sur `postgres:19beta1`, juin 2026)** : PG19 introduit la commande **`REPACK`** (« rewrite a table to reclaim disk space »), avec options `VERBOSE | ANALYZE | CONCURRENTLY` et `USING INDEX` (recluster) ; `VACUUM FULL` coexiste. `REPACK CONCURRENTLY` = l'online repack enfin en core. → **Piste v0.7+** : sur PG19+, `qwash repack` pourrait **déléguer à `REPACK CONCURRENTLY` natif** (dispatcher version-aware), et garder sa méthode incrémentale sur PG ≤18. L'avantage de qwash est donc maximal sur **PG ≤18** (pas d'online repack natif).

- [ ] **Modèle de sortie « finding avec sévérité »** · P0 · ~moyen
  - Décider maintenant (l'index est le premier cas, il fixe le pattern). Chaque check → `OK | WARNING | CRITICAL` + valeur + seuil, en texte ET `--json` (pour alimenter un rapport). Le bloat existant y rentre déjà (CRITICAL/HIGH/MEDIUM).
    ```
    INDEX HEALTH
      [CRITICAL] Invalid indexes   : 2  (failed CONCURRENTLY leftovers)
      [WARNING]  Unused indexes    : 12 (4.1 GB) — since stats reset 2026-05-30
      [WARNING]  Redundant indexes : 3  (1.2 GB)
    ```
  - **Action** : définir la struct `Finding` (severity, label, value, detail, hint) + le rendu texte/JSON. Seuils OK/WARN/CRIT à caler avec la pratique d'audit réelle.

### 6.2 Reporting des index (`--indexes`) — première section neuve

- [ ] **Section `--indexes` : santé des index (read-only)** · P0 · ~moyen
  - Approfondit un domaine déjà maîtrisé (bloat btree + détection d'index invalides via le nettoyage `_ccnew`). Source d'inspiration : `~/DALIBO/CCC/audit_pg.sql` (sections « index inutilisés / invalides / redondants »).
  - Trois checks, **avec la couche d'honnêteté qui fait la crédibilité** :
    - **Inutilisés** : `idx_scan = 0` (`pg_stat_user_indexes`) — MAIS *depuis le dernier reset des stats* (afficher la date), **exclure les index qui portent une contrainte PK/unique** (non droppables tels quels), **signaler les index récemment créés** (faussement inutilisés), et **noter le biais réplica** (les scans sur un standby ne comptent pas).
    - **Redondants** : index dont les colonnes sont préfixe d'un autre sur la même table — MAIS attention aux **faux positifs** : index partiels, opclasses différentes, colonnes `INCLUDE`.
    - **Invalides** : `indisvalid = false` (qwash les connaît déjà).
  - **Action** : requêtes catalogue paramétrées (réutiliser le pattern db/ durci), rendu en findings, tests d'intégration sous la matrice 14-18.
  - **Synergies** : `qwash audit --indexes` → remédiation `qwash reindex` ; complète `qwash audit --bloat` (bloat des index déjà fait).

---

## 0.7.0+ — Sections d'audit additionnelles (une à la fois)

> Cartographiées depuis `~/DALIBO/CCC/audit_pg.sql` (~35 checks). Chacune = une section read-only, même modèle de findings, même rigueur. La perf requêtes (`pg_stat_statements`) reste surtout le terrain de **quellog** côté logs.

- [ ] **`--maintenance`** · P1 — dead tuples, tables jamais vacuum/analyze, stats périmées, reset des stats. *Synergie forte avec le stale-stats du hardening ; remédiation `qwash vacuum`/`analyze`.*
- [ ] **`--storage`** · P1 — tailles bases/tablespaces, top tables, compteurs d'objets, fichiers orphelins.
- [ ] **`--config` / `--server`** · P1 — mémoire, WAL/archivage, checkpoints/bgwriter, logging, autovacuum, checksums, `shared_preload_libraries`, timeouts, parallélisme (non-défaut + risqués).
- [ ] **`--security`** · P1 — rôles md5 (→ scram-sha-256), `pg_hba.conf`, droits.
- [ ] **`--replication`** · P2 — état réplication, slots et WAL retenu, archivage.
- [ ] **`--connections` / `--locks` / `--activity`** · P2 — sessions par état, idle-in-transaction > seuil, verrous, transactions longues. *Plusieurs helpers déjà internes (`checkLongTransactions`, `checkTableLocks`).*
- [ ] **Mode rapport agrégé** · P2 — `qwash audit` complet → un rapport unique (texte/JSON/markdown) exploitable en livrable client, homogène avec quellog.
- [ ] **Routines de maintenance** · P2 — `qwash vacuum`/`analyze` (au-delà de `repack`/`reindex`), p.ex. « analyser les tables aux stats périmées détectées par l'audit ». Toujours via le préflight de sûreté.

---

## Dette technique différée (du hardening, non bloquante)

- [ ] **`go test ./...` autonome** · P1 · ~petit — un `TestMain` qui build `bin/qwash` + `t.Skip` propre si pas de PostgreSQL ; aujourd'hui la suite exige un binaire pré-buildé et une instance vivante (piège local, masqué par la CI). *Filet à poser avant la grosse refonte CLI.*
- [ ] **Propager le `ctx` annulable** · P2 · ~moyen — plusieurs requêtes catalogue/verrou (`ListDatabases`, `acquireTableLock`, helpers) tournent encore sur `context.Background()` ; l'annulation Ctrl-C n'atteint que la boucle de compaction.
- [ ] **Triggers `ENABLE ALWAYS`/`REPLICA`** · P2 — actuellement avertis mais non bloqués pendant `repack` (se déclenchent sur chaque ligne déplacée). Option de confirmation/refus.
- [ ] **Ménage repo** · P2 — branches mortes (`feature/btree_bloat`, `feature/gin_bloat`), fichiers `_*` locaux (`db/_query_bench_test.go` casse + référence l'ancien module path), commentaire faux sur `ListTables`.
- [ ] **Réconcilier le claim « PostgreSQL 9.6+ »** · P2 — la CI ne valide que 14-18, et `--reindex` exige 12+. Soit corriger le README, soit étendre la matrice avec des `t.Skip` version-aware.
- [ ] **Asymétrie estimate/debloat sur homonymes** · P2 — `--estimate -t x` matche par nom nu (tous schémas) tandis que la mutation résout via search_path. Cosmétique en lecture seule.

---

## Plan de release

1. **`0.5.0` (cette semaine)** — tagger le hardening tel quel : bump version, figer `[Unreleased]` → `[0.5.0] - <date>`, MAJ du `VERSION=0.4.0` du README. Les correctifs de fiabilité partent en clientèle sans attendre.
2. **`0.6.0`** — fondation audit (§6) : refonte CLI non-breaking, puis section `--indexes`. Changelog clair « nouvelle CLI » ≠ « corrections de fiabilité ».
3. **`0.7.0+`** — sections d'audit additionnelles au fil de l'eau.
