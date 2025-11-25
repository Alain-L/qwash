# Benchmark: VACUUM FULL vs qwash
**Date**: 2025-11-23
**PostgreSQL**: 16
**Tables**: 3 tables identiques (90,000 rows, 9 columns)
**Bloat initial**: ~59 MB (~83% bloat après 5 passes UPDATE/DELETE/INSERT)

---

## 📊 Résultats

| Méthode | Temps Exécution | Taille Initiale | Taille Finale | Bloat Final | Reduction |
|---------|----------------|-----------------|---------------|-------------|-----------|
| **VACUUM FULL** | **0.385s** | 59 MB | 10.1 MB | 0.00% | **82.9%** |
| **qwash** | **9.287s** | 59 MB | 11.0 MB | 11.43% | **81.4%** |
| **pgcompacttable** | _(non testé)_ | 59 MB | 59 MB | 83.14% | 0% |

---

## 🔍 Analyse Détaillée

### VACUUM FULL ✅
- **Vitesse**: ⚡ **24x plus rapide** que qwash
- **Qualité**: 🎯 **Parfait** - 0% bloat final
- **Taille**: 📦 10.1 MB (optimal)
- **Inconvénient**: 🔒 Bloque la table (ACCESS EXCLUSIVE lock)

### qwash ⚠️
- **Vitesse**: 🐌 9.3s (24x plus lent)
- **Qualité**: 🟡 **Bon** - 11.43% bloat résiduel
- **Taille**: 📦 11.0 MB (+9% vs VACUUM FULL)
- **Avantage**: 🔓 Non-bloquant (théoriquement)
- **Problème actuel**: Utilise un verrou transaction (bloquant en pratique)

### pgcompacttable ❌
- **Statut**: Non exécuté (table non débloatée)
- **Bloat**: 83.14% (inchangé)

---

## 🔎 Diagnostic qwash

### Pourquoi 11.43% de bloat résiduel?

Le bloat résiduel de qwash s'explique par:

1. **Algorithme par pages**
   ```
   Estimation: 982 bloat pages (1291 - 309)
   Réalité: 165 pages de bloat restantes (1444 - 1279)
   ```
   - qwash traite les pages les plus hautes
   - Mais le bloat peut être fragmenté à travers toutes les pages
   - Résultat: certaines pages à moitié vides subsistent

2. **Pas de compaction totale**
   - VACUUM FULL réécrit la table entière (compaction parfaite)
   - qwash déplace seulement les tuples des pages hautes → basses
   - Les "trous" dans les pages basses ne sont pas comblés

3. **CTID final: (1443,47)**
   - Pages théoriques minimum: 1279
   - Pages réelles après qwash: 1444
   - **Overhead**: 165 pages = 1.3 MB = 11.43% bloat

---

## ⚡ Problème de Performance

### Pourquoi qwash est 24x plus lent?

**Analyse du log qwash:**
```
🔁 197 iterations needed (processing 5 pages/round)
```

**Calcul:**
- 197 itérations × ~45ms/itération ≈ 8.9s
- VACUUM tous les 250 pages = ~4 VACUUM pendant l'opération

**vs VACUUM FULL:**
- 1 seule opération bulk de réécriture
- Optimisé au niveau noyau PostgreSQL
- 0.385s total

---

## 🎯 Conclusions

### Contexte d'Utilisation

| Scénario | Recommandation | Justification |
|----------|----------------|---------------|
| **Maintenance hors-ligne** | VACUUM FULL | 24x plus rapide, 0% bloat, parfait |
| **Production 24/7** | qwash (si non-bloquant) | Acceptable si vraiment non-bloquant |
| **Bloat critique** | VACUUM FULL | Qualité supérieure |
| **Bloat modéré** | AUTOVACUUM | Préventif, suffisant |

### État Actuel de qwash

✅ **Points Forts:**
- Algorithme fonctionnel
- Réduit le bloat de 81.4%
- Code transparent et maintenable

⚠️ **Points d'Amélioration:**
1. **Performance**:
   - Grouper davantage les opérations
   - Réduire le nombre d'itérations
   - Considérer batch INSERT au lieu de row-by-row

2. **Qualité du débloat**:
   - Implémenter un second passage pour combler les trous
   - Ou accepter le bloat résiduel et le documenter clairement

3. **Non-blocking** (critique):
   - Actuellement utilise une transaction → BLOQUE
   - Besoin: opérations hors transaction avec retry logic
   - Référence: implémentation pg_repack

---

## 📈 Comparaison Historique

### Benchmark Précédent (2025-11-23 AM)

Sur `high_bloat_table_300x` (bloat initial 28.81%):
- **qwash**: 32s, 0% bloat final
- **pgcompacttable**: 68s, ~0% bloat
- **VACUUM FULL**: Non testé

**Conclusion précédente**: qwash 2x plus rapide que pgcompacttable ✅

### Benchmark Actuel (2025-11-23 PM)

Sur `test_*` (bloat initial 83.14%):
- **VACUUM FULL**: 0.385s, 0% bloat final ✅
- **qwash**: 9.287s, 11.43% bloat
- **pgcompacttable**: Non exécuté

**Conclusion actuelle**: VACUUM FULL 24x plus rapide, qualité supérieure

---

## 🚀 Recommandations

### Court Terme

1. **Documenter les limites**
   ```
   README.md: "qwash est actuellement bloquant et plus lent que VACUUM FULL.
   Utilisez VACUUM FULL pour la production jusqu'à la v1.0."
   ```

2. **Benchmarks réalistes**
   - Comparer avec pgcompacttable sur mêmes données
   - Tester sur tables > 1 GB
   - Mesurer l'impact du verrou sur production

3. **Tests de régression**
   - Vérifier que bloat résiduel < 15%
   - Alerter si dégradation

### Moyen Terme

1. **Optimisation vitesse**
   - Passer de 5 pages/round à 50 pages/round
   - Batch INSERT avec COPY au lieu de INSERT INTO ... SELECT
   - Réduire la fréquence de VACUUM (tous les 1000 pages?)

2. **Optimisation qualité**
   - Second passage: itérer sur pages restantes pour combler
   - Ou VACUUM FULL final (si acceptable)

### Long Terme

1. **Vraie implémentation non-bloquante**
   - Étudier architecture pg_repack
   - Triggers pour capturer les changements pendant la compaction
   - Table shadow + switch atomique à la fin

2. **Support TOAST et index**
   - REINDEX CONCURRENTLY pour les index
   - Compaction TOAST séparée

---

## 📝 Notes Méthodologiques

### Création du Bloat

```sql
-- 5 passes UPDATE/DELETE/INSERT
DO $$
BEGIN
  FOR i IN 1..5 LOOP
    UPDATE test_qwash SET col3 = col3 || 'x' WHERE id % 2 = 0;
    DELETE FROM test_qwash WHERE id % 2 = 0;
    INSERT INTO test_qwash SELECT * FROM source WHERE id % 2 = 0;
  END LOOP;
END $$;
```

**Résultat**: 83% bloat (59 MB pour 90k rows)

### Mesure du Temps

```bash
time psql -c "VACUUM FULL test_vacuumfull;"
# real: 0m0.385s

time ./bin/qwash --debloat -t test_qwash
# real: 0m9.287s
```

### Mesure du Bloat

Utilisation de `sql/table_bloat.sql` (version CTE):
```sql
bloat_pct = ROUND(
  (100.0 * GREATEST(0, actual_pages - estimated_min_pages)
   / NULLIF(actual_pages, 0)
  )::numeric, 2
)
```

---

## 🎓 Conclusion Générale

**qwash v0.1 est un POC réussi** qui démontre:
- ✅ Compréhension de l'algorithme de compaction
- ✅ Code transparent et maintenable
- ✅ Réduction significative du bloat (81.4%)

**Mais n'est pas encore production-ready** car:
- ❌ 24x plus lent que VACUUM FULL
- ❌ Bloquant (malgré l'objectif non-bloquant)
- ❌ Bloat résiduel de 11%

**Prochaine étape**: Décider de la direction du projet:
1. Continuer vers une vraie implémentation non-bloquante (gros effort)
2. Pivoter vers un outil de monitoring/reporting (valeur immédiate)
3. Documenter comme référence éducative (transparence SQL)

**Recommandation**: Option 2 + 3
- Le mode `--estimate` est excellent et production-ready
- La transparence des requêtes SQL est une vraie valeur ajoutée
- Le mode `--debloat` reste expérimental et éducatif
