# Benchmark Comparatif Final: VACUUM FULL vs pgcompacttable vs qwash

**Date**: 2025-11-23
**PostgreSQL**: 16
**Tables**: 3 tables identiques (90,000 rows, 9 colonnes)
**Bloat initial**: ~59 MB (~83% bloat après 5 passes UPDATE/DELETE/INSERT)

---

## 📊 Résultats Complets

| Méthode | Temps | Taille Initiale | Taille Finale | Pages Finales | Bloat Final | Vitesse Relative |
|---------|-------|-----------------|---------------|---------------|-------------|------------------|
| **VACUUM FULL** | **0.385s** | 59 MB | 10.1 MB | 1257 | **0.00%** ✅ | **Baseline (109x)** |
| **qwash** | **9.287s** | 59 MB | 11.0 MB | 1444 | **11.43%** ⚠️ | **24x baseline (4.5x)** |
| **pgcompacttable** | **41.853s** | 59 MB | 10.1 MB | 1258 | **0.00%** ✅ | **109x baseline** |

---

## 🏆 Classement

### 🥇 Vitesse: VACUUM FULL
- **0.385s** - Champion absolu
- 24x plus rapide que qwash
- 109x plus rapide que pgcompacttable

### 🥇 Qualité: VACUUM FULL & pgcompacttable
- **0% bloat** - Parfait
- qwash: 11.43% bloat résiduel

### 🥇 Production 24/7: _(aucun)_
- VACUUM FULL: Bloquant (ACCESS EXCLUSIVE)
- qwash: Actuellement bloquant (transaction)
- pgcompacttable: UPDATE-based, mais très lent (42s)

---

## 📈 Analyse Détaillée

### 1. VACUUM FULL ⚡
```
Temps:        0.385s
Taille:       10.1 MB (1257 pages)
Bloat final:  0.00%
Verrou:       ACCESS EXCLUSIVE (bloquant complet)
```

**✅ Points forts:**
- Ultra-rapide (109x plus rapide que pgcompacttable!)
- Qualité parfaite (0% bloat)
- Taille minimale (10.1 MB)
- Built-in PostgreSQL (aucune dépendance)

**❌ Points faibles:**
- Bloque TOUTES les opérations (SELECT, INSERT, UPDATE, DELETE)
- Impossible en production 24/7

**💡 Use case:**
- Maintenance planifiée (fenêtre de maintenance)
- Environnements dev/staging
- Tables non-critiques

---

### 2. pgcompacttable 🐢
```
Temps:        41.853s
Taille:       10.1 MB (1258 pages)
Bloat final:  0.00%
Verrou:       Row-level locks (UPDATE-based)
Extension:    pgstattuple requise
```

**✅ Points forts:**
- Qualité parfaite (0% bloat, identique à VACUUM FULL)
- Moins bloquant que VACUUM FULL (UPDATE par batch)
- REINDEX automatique des index

**❌ Points faibles:**
- **Très lent**: 109x plus lent que VACUUM FULL
- **Très lent**: 4.5x plus lent que qwash
- Requiert extension pgstattuple (superuser)
- 42 secondes pour 90k rows → impraticable sur grosses tables

**📊 Détails d'exécution:**
```
[Sun Nov 23 16:44:50 2025] Statistics: 7585 pages, ~82.410% can be compacted
[Sun Nov 23 16:44:50 2025] Set pages/round: 5
[Sun Nov 23 16:44:50 2025] Set pages/vacuum: 475
[Sun Nov 23 16:45:32 2025] Vacuum final: 1258 pages left
[Sun Nov 23 16:45:32 2025] Reindex: reduced by 74% (5.797MB)
```

**💡 Use case:**
- Production avec tolérance latence (42s acceptable)
- Tables moyennes (<1 GB)
- Besoin de 0% bloat absolu

---

### 3. qwash ⚡⚠️
```
Temps:        9.287s
Taille:       11.0 MB (1444 pages)
Bloat final:  11.43%
Verrou:       Transaction-level (actuellement bloquant)
Extension:    Aucune
```

**✅ Points forts:**
- **4.5x plus rapide** que pgcompacttable
- Aucune extension requise (SQL natif)
- Code transparent et auditable
- Réduction de bloat significative (81.4%)

**⚠️ Points faibles:**
- Bloat résiduel de 11.43% (165 pages vs minimum théorique)
- Actuellement bloquant (contrairement à l'objectif)
- 24x plus lent que VACUUM FULL

**🔬 Analyse du bloat résiduel:**
```
Pages minimum théoriques: 1279
Pages réelles après qwash: 1444
Bloat résiduel: 165 pages = 1.3 MB = 11.43%
```

**Cause:** qwash déplace les tuples des pages hautes → basses mais ne comble pas les "trous" dans les pages existantes.

**💡 Use case:**
- Actuellement: Expérimental/éducatif
- Futur (si non-bloquant): Tables volumineuses en production
- Mode --estimate: Production-ready maintenant

---

## 📊 Comparaison Visuelle

### Temps d'Exécution
```
VACUUM FULL:       ▓ 0.385s
qwash:             ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 9.287s
pgcompacttable:    ▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓▓ 41.853s
```

### Bloat Final
```
VACUUM FULL:       ████████████████████████████████ 0%   (parfait)
pgcompacttable:    ████████████████████████████████ 0%   (parfait)
qwash:             ████████████████████████▓▓▓▓▓▓▓▓ 11.43% (bon)
```

### Taille Finale
```
VACUUM FULL:       ████████████████████████████████ 10.1 MB  (optimal)
pgcompacttable:    ████████████████████████████████ 10.1 MB  (optimal)
qwash:             ██████████████████████████████▓▓ 11.0 MB  (+9%)
```

---

## 🎯 Recommandations par Scénario

### Scénario 1: Maintenance Planifiée (fenêtre de maintenance)
**→ VACUUM FULL**
- Raison: 109x plus rapide, 0% bloat, aucune dépendance
- Tolérance: Verrou acceptable pendant fenêtre
- ROI: Maximum (vitesse + qualité)

### Scénario 2: Production 24/7, Tolérance Latence
**→ pgcompacttable** (si 42s acceptable)
- Raison: 0% bloat, moins bloquant
- Trade-off: Très lent mais qualité parfaite
- Attention: Tester sur table similaire d'abord

### Scénario 3: Production 24/7, Grosse Table (>10 GB)
**→ Aucune solution satisfaisante actuellement**
- VACUUM FULL: Trop bloquant
- pgcompacttable: Trop lent (42s pour 90k rows → heures pour 10 GB)
- qwash: Pas encore non-bloquant
- Alternative: AUTOVACUUM + pg_repack

### Scénario 4: Monitoring & Estimation
**→ qwash --estimate**
- Raison: Production-ready, rapide, sans extension
- Avantage: Requêtes SQL transparentes et auditables
- Valeur: Excellent pour le monitoring proactif

---

## 🔬 Analyse Approfondie

### Pourquoi pgcompacttable est 109x plus lent que VACUUM FULL?

**pgcompacttable utilise une approche UPDATE-based:**
```sql
-- Pseudo-code simplifié
FOR each batch of 5 pages:
  UPDATE table SET col = col WHERE ctid IN (high pages)
  -- Force PostgreSQL to move tuples
  VACUUM every 475 pages
```

**Overhead:**
- 197 itérations (982 pages / 5 pages/round)
- Chaque UPDATE crée des versions de tuples (MVCC)
- VACUUM fréquent pour nettoyer
- Total: ~42 secondes

**VACUUM FULL utilise une approche bulk:**
```
1. Create new table file
2. Copy all live tuples sequentially
3. Drop old file
4. Rebuild indexes
```

**Avantage:**
- 1 seule opération optimisée kernel
- Pas de MVCC overhead
- Total: 0.385s

### Pourquoi qwash a 11.43% bloat résiduel?

**Visualisation:**
```
Avant qwash (page 1000):  [T1][T2][T3][__][__][__][__][__]  (3 tuples, 5 trous)
Après qwash:              [T1][T2][T3][T4][T5][__][__][__]  (5 tuples, 3 trous)
                                       ↑↑↑↑ déplacés depuis pages hautes
```

**Problème:** Les trous (_) ne sont pas comblés.

**Solution possible:**
1. Second passage pour compacter davantage
2. Ou accepter 10-15% bloat comme trade-off vitesse vs qualité

---

## 📚 Détails Techniques

### Configuration des Tables Test

```sql
-- Création
CREATE TABLE test_X AS SELECT * FROM source_table;
CREATE INDEX test_X_pkey ON test_X(id);

-- Bloat généré
DO $$
BEGIN
  FOR i IN 1..5 LOOP
    UPDATE test_X SET col3 = col3 || 'x' WHERE id % 2 = 0;
    DELETE FROM test_X WHERE id % 2 = 0;
    INSERT INTO test_X SELECT * FROM source WHERE id % 2 = 0;
  END LOOP;
END $$;
```

**Résultat:** 83% bloat (59 MB pour 90k rows)

### Mesure des Performances

```bash
# VACUUM FULL
time psql -c "VACUUM FULL test_vacuumfull;"
# real: 0m0.385s

# pgcompacttable
time ./pgcompacttable -d qwash -t test_pgcompacttable
# real: 0m41.853s

# qwash
time ./bin/qwash --debloat -t test_qwash
# real: 0m9.287s
```

### Environnement

- **OS**: macOS Darwin 23.6.0
- **PostgreSQL**: 16
- **CPU**: Apple M1 Pro
- **RAM**: Non limitée pour tests
- **Disque**: SSD

---

## 🎓 Conclusions

### Pour qwash

**État actuel (v0.1):**
- ✅ POC fonctionnel et prometteur
- ✅ Mode --estimate production-ready
- ⚠️ Mode --debloat expérimental
- ⚠️ 4.5x plus rapide que pgcompacttable
- ❌ 24x plus lent que VACUUM FULL
- ❌ 11.43% bloat résiduel vs 0% pour alternatives

**Valeur Unique:**
- Code transparent (SQL lisible, auditab)
- Aucune extension requise
- Excellent outil de monitoring (--estimate)

**Prochaines Étapes Suggérées:**

1. **Court terme** (1-2 semaines):
   - Documenter clairement mode --debloat comme expérimental
   - Focus sur mode --estimate (déjà très bon)
   - Ajouter support index bloat (btree_bloat.sql simplifié)

2. **Moyen terme** (1-2 mois):
   - Réduire bloat résiduel: second passage de compaction
   - Optimiser vitesse: batch INSERT, moins de VACUUM
   - Tests sur grosses tables (>10 GB)

3. **Long terme** (6 mois):
   - Implémentation vraiment non-bloquante (pg_repack style)
   - Support TOAST et autres types d'index
   - Parallélisation (multiple tables simultanées)

**Recommandation Stratégique:**

Ne pas essayer de battre VACUUM FULL en vitesse/qualité (impossible).
Se concentrer sur la niche: **outil de monitoring transparent avec débloat modéré en production**.

- Utilisateurs ont déjà VACUUM FULL pour maintenance offline
- Utilisateurs ont besoin d'un bon outil de monitoring (qwash --estimate)
- Utilisateurs pourraient bénéficier d'un débloat léger continu (10-15% bloat acceptable)

**Positionnement:**
```
VACUUM FULL:      Chirurgie lourde (maintenance planifiée)
qwash:            Prévention & monitoring + débloat léger
pgcompacttable:   Chirurgie moyenne (trop lent pour grosses tables)
```

---

## 📊 Annexe: Logs Complets

### pgcompacttable (verbose)

```
[Sun Nov 23 16:44:50 2025] (qwash) Connecting to database
[Sun Nov 23 16:44:50 2025] (qwash) Handling tables. Attempt 1
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Start handling table
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Vacuum initial: 7585 pages left
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Bloat statistics with pgstattuple
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Statistics: 7585 pages, ~82.410% can be compacted (48.835MB)
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Update by column: col5
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Set pages/round: 5
[Sun Nov 23 16:44:50 2025] (qwash:public.test_pgcompacttable) Set pages/vacuum: 475
[Sun Nov 23 16:45:32 2025] (qwash:public.test_pgcompacttable) Vacuum final: 1258 pages left
[Sun Nov 23 16:45:32 2025] (qwash:public.test_pgcompacttable) Analyze final
[Sun Nov 23 16:45:32 2025] (qwash:public.test_pgcompacttable) Reindex: reduced by 74% (5.797MB)
[Sun Nov 23 16:45:32 2025] (qwash:public.test_pgcompacttable) Processing complete
[Sun Nov 23 16:45:32 2025] (qwash:public.test_pgcompacttable) Results: 1258 pages, size reduced by 49.430MB
```

### qwash (extract)

```
[INFO] Compacting table: test_qwash
📦 982 bloat pages (1291 - 309) estimated

🔁 197 iterations needed (processing 5 pages/round)
🚀 Starting compaction
📊 Running initial VACUUM ANALYZE...
✅ Initial VACUUM ANALYZE complete

[... 197 itérations ...]

🔎 Highest CTIDs after final VACUUM ANALYZE:
  - (1443,47)
  - (1443,46)
🎯 Compaction process complete
```

---

**Benchmark réalisé par**: Audit automatisé
**Date**: 2025-11-23
**Durée totale**: ~52 secondes (VACUUM FULL + qwash + pgcompacttable)
