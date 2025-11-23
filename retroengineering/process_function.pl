#Process function

sub process {
  my $schema_name = shift;
  my $table_name = shift;
  my $attempt = shift;
  my $table_info = shift;

  my $is_skipped;
  my $is_locked = try_advisory_lock($schema_name, $table_name) ? 0 : 1;

  if ($DBI::err) {
    logger(LOG_ERROR, "Table handling interrupt.");
    return -1;
  }

  # Taille avant VACUUM initial
  $table_info->{stats} = get_size_stats($schema_name, $table_name); 

  if ($DBI::err) {
    logger(LOG_ERROR, "Table handling interrupt.");
    return -1;
  }

  $table_info->{base_stats} = {%{$table_info->{stats}}} unless ($table_info->{base_stats});

  if (!$is_locked && !$no_initial_vacuum) {
    my $vacuum_time = time;
    vacuum($schema_name, $table_name); # VACUUM initial

    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt.");
      return -1;
    }    

    $vacuum_time = time - $vacuum_time;
    $table_info->{stats} = get_size_stats($schema_name, $table_name); # Taille après vacuum initial
    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt."); 
      return -1;
    }

    logger(LOG_NOTICE, "Vacuum initial: %d pages left, duration %.3f seconds.", ($table_info->{stats}{page_count}||0), $vacuum_time);

    if($table_info->{stats}{page_count} <= 1) {
      logger(LOG_NOTICE, "Skipping processing: empty or 1 page table.");
      $is_skipped = 1;
    }
  }

  my $bloat_stats;
  my $is_reindexed;
  if (!$is_locked && !$is_skipped) {
    
    if ($initial_reindex && !$no_reindex && defined($attempt) && $attempt == 0) {
      $is_reindexed = reindex_table($table_name, $schema_name, $db_name);
    }

    my $get_stat_time = time;
    $bloat_stats = get_bloat_stats($schema_name, $table_name); ## récupération des stats de bloat

    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt.");
      return -1;
    }
 
    $get_stat_time = time - $get_stat_time;
    if ($bloat_stats->{effective_page_count}) {
      logger(LOG_NOTICE,"Bloat statistics with pgstattuple: duration %.3f seconds.", $get_stat_time);
    } else {
      my $analyze_time = time;
      analyze($schema_name, $table_name); ## ANALYZE
      
      if ($DBI::err) {
        logger(LOG_ERROR, "Table handling interrupt.");
        return -1;
      }

      $analyze_time = time - $analyze_time;
      logger(LOG_WARNING, "Analyze required initial: duration %.3f second.", $analyze_time);
      $get_stat_time = time;
      $bloat_stats = get_bloat_stats($schema_name, $table_name); ## récupération des stats de bloat
      
      if ($DBI::err) {
        logger(LOG_ERROR, "Table handling interrupt.");
        return -1;
      }

      $get_stat_time = time - $get_stat_time;
      if ($bloat_stats->{effective_page_count}) {
        logger(LOG_NOTICE, "Bloat statistics with pgstattuple: duration %.3f seconds.", $get_stat_time)
      } else {
        logger('qiuet', "Can not get bloat statistics, processing stopped.");
        $is_skipped = 1;  
      }
    }
  }

  if (!$is_locked && !$is_skipped) {
    # vérification que la table est bien bloat
    my $can_be_compacted = ($bloat_stats->{'free_percent'} > 0 && ($table_info->{stats}{page_count} > $bloat_stats->{effective_page_count}));
    if ($can_be_compacted) {
      logger(LOG_WARNING, "Statistics: %d pages (%d pages including toasts and indexes), it is expected that ~%0.3f%% (%d pages) can be compacted with the estimated space saving being %s.",
        $table_info->{stats}{page_count},
        $table_info->{stats}{total_page_count},
        $bloat_stats->{'free_percent'},
        ($table_info->{stats}{page_count} - $bloat_stats->{effective_page_count}),
        nice_size($bloat_stats->{free_space})
      );
    } else {
      logger(LOG_WARNING, "Statistics: %d pages (%d pages including toasts and indexes)",
        $table_info->{stats}{page_count},
        $table_info->{stats}{total_page_count}
      );
    }

    if(has_triggers($schema_name, $table_name)) {
      
      if ($DBI::err) {
        logger(LOG_ERROR, "Table handling interrupt.");
        return -1;
      }

      logger(LOG_ERROR,'Can not process: "always" or "replica" triggers are on.');
      $is_skipped = 1;
    }

    if(!$force) {      
      if($table_info->{stats}{page_count} < MINIMAL_COMPACT_PAGES) {
        logger(LOG_NOTICE,"Skipping processing: %d pages from %d pages minimum required.", $table_info->{stats}{page_count}, MINIMAL_COMPACT_PAGES);
        $is_skipped = 1;
      }
      if(($bloat_stats->{free_percent}||0) <= MINIMAL_COMPACT_PERCENT) {
        logger(LOG_NOTICE,"Skipping processing: %0.2f%% space to compact from 20%% minimum required.",($bloat_stats->{free_percent}||0));
        $is_skipped = 1;  
      }
    }
  }

  if (!$is_locked && !$is_skipped) {
    logger(LOG_WARNING, "Processing forced.") if ($force);
    my $vacuum_page_count = 0;
    my $initial_size_stats = {%{$table_info->{stats}}};
    my $to_page = $table_info->{stats}{page_count} - 1; # On commence par la dernière page
    my $progress_report_time = time;
    my $clean_pages_total_duration = 0;
    my $last_loop = $table_info->{stats}{page_count} + 1;
    my $expected_error_occurred = 0;

    my $expected_page_count = $table_info->{stats}{page_count};
    my $column_name = get_update_column($schema_name, $table_name);

    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt.");
      return -1;
    }

    my $pages_per_round = get_pages_per_round($table_info->{stats}{page_count}, $to_page); ## pages par tour
    my $pages_before_vacuum = get_pages_before_vacuum($table_info->{stats}{page_count}, $expected_page_count); ## pages par vacuum

    my $max_tupples_per_page = get_max_tupples_per_page($schema_name, $table_name);

    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt.");
      return -1;
    }

    logger(LOG_NOTICE, "Update by column: %s.", $column_name||'');
    logger(LOG_NOTICE, "Set pages/round: %d.",  $pages_per_round);
    logger(LOG_NOTICE, "Set pages/vacuum: %d.", $pages_before_vacuum);

    my $start_time;
    my $last_to_page;
    my $loop;
    for ($loop = $table_info->{stats}{page_count}; $loop > 0; $loop--) {
      $start_time = time;
      _dbh->begin_work;
      $last_to_page = $to_page;

      # LA FONCTION DE NETTOYAGE
      $to_page = clean_pages($schema_name, $table_name, $column_name, $last_to_page, $pages_per_round,  $max_tupples_per_page); 
      $clean_pages_total_duration += (time - $start_time);

      unless(defined($to_page)) {
        _dbh->rollback();

        if ($DBI::err && $DBI::errstr =~ 'deadlock detected') {
          logger(LOG_ERROR,"Detected deadlock during cleaning.");
          next;
        } elsif ($DBI::err && $DBI::errstr =~ 'cannot extract system attribute') {
          logger(LOG_ERROR, "System attribute extraction error has occurred, processing stopped.");
          $expected_error_occurred = 1;
          last;
        } elsif($DBI::err) {
          logger(LOG_ERROR,"Cannot handle table: %s", $DBI::errstr);
          return -1;
        } else {
          logger(LOG_ERROR, "Incorrect result of cleaning: column_name %s, to_page %d, pages_per_round %d, max_tupples_per_page %d.",$column_name, $last_to_page, $pages_per_round, $max_tupples_per_page);
        }
      } else {
        if ($to_page == -1) {
          _dbh->rollback();
          $to_page = $last_to_page;
          last;
        }
        _dbh->commit();
      } 

      my $sleep_time = $delay_ratio * (time - $start_time);
      # This is amazing, but sleep_time may unexpectedly be less than 0. Spotted on windows
      if ($sleep_time > 0) {
          sleep($sleep_time);
      }
      
      if (_after_round_statement) {
        _after_round_statement->execute();

        if ($DBI::err) {
          logger(LOG_ERROR, "SQL Error in after round statement: %s",  $DBI::errstr);
        }
      }

      if (time - $progress_report_time >= PROGRESS_REPORT_PERIOD && $last_to_page != $to_page) {
        logger(LOG_WARNING, "Progress: %s %d pages completed.", (defined $bloat_stats->{effective_page_count} ? int(100 * ($to_page ? ($initial_size_stats->{page_count} - $to_page - 1) / ($table_info->{base_stats}{page_count} - $bloat_stats->{effective_page_count}) : 1) ).'%, ' : ' '), ($table_info->{stats}{page_count} - $to_page - 1));
        $progress_report_time = time;
      }

      $expected_page_count -= $pages_per_round;
      $vacuum_page_count += ($last_to_page - $to_page);
      
      if ($routine_vacuum && $vacuum_page_count >= $pages_before_vacuum) {
        my $duration = $clean_pages_total_duration / ($last_loop - $loop);
        my $average_duration = $duration == 0 ? 0.0001 : $duration;
        logger(LOG_WARNING, "Cleaning in average: %.1f pages/second (%.3f seconds per %d pages).", ($pages_per_round / $average_duration), $duration, $pages_per_round);
        $clean_pages_total_duration = 0;
        $last_loop = $loop;

        my $vacuum_time = time;
        vacuum($schema_name, $table_name);
        
        if ($DBI::err) {
          logger(LOG_ERROR, "Table handling interrupt.");
          return -1;
        }

        $vacuum_time = time - $vacuum_time;

        $table_info->{stats} = get_size_stats($schema_name, $table_name);

        if ($DBI::err) {
         logger(LOG_ERROR, "Table handling interrupt.");
          return -1;
        }

        if ($table_info->{stats}{page_count} > $to_page + 1) {
          logger(LOG_NOTICE, "Vacuum routine: can not clean %d pages, %d pages left, duration %0.3f seconds.", ($table_info->{stats}{page_count} - $to_page - 1), $table_info->{stats}{page_count}, $vacuum_time);
        } else {
          logger(LOG_NOTICE, "Vacuum routine: %d pages left, duration %.3f seconds.", ($table_info->{stats}{page_count}||0), $vacuum_time);
        }

        $vacuum_page_count = 0;

        my $last_pages_before_vacuum = $pages_before_vacuum;
        $pages_before_vacuum = get_pages_before_vacuum($table_info->{stats}{page_count}, $expected_page_count); 
        if ($last_pages_before_vacuum != $pages_before_vacuum) {
          logger(LOG_WARNING, "Set pages/vacuum: %d.", $pages_before_vacuum);
        }
      }

      if ($to_page >= $table_info->{stats}{page_count}) {
        $to_page = $table_info->{stats}{page_count} - 1;
      }

      if ($to_page <= 1) {
        $to_page = 0;
        last;
      }

      my $last_pages_per_round = $pages_per_round;
      $pages_per_round = get_pages_per_round($table_info->{stats}{page_count}, $to_page);
        
      if ($last_pages_per_round != $pages_per_round) {
        logger(LOG_WARNING, "Set pages/round: %d.",  $pages_per_round); 
      }
    }

    if ($loop == 0) {
      logger(LOG_WARNING, "Maximum loops reached.");
    }

    if ($to_page > 0) {
      my $vacuum_time = time;
      sleep(1);
      vacuum($schema_name, $table_name);

      if ($DBI::err) {
        logger(LOG_ERROR, "Table handling interrupt.");
        return -1;
      }

      $vacuum_time = time - $vacuum_time;

      $table_info->{stats} = get_size_stats($schema_name, $table_name);

      if ($DBI::err) {
        logger(LOG_ERROR, "Table handling interrupt.");
        return -1;
      }
 
      if ($table_info->{stats}{page_count} > $to_page + $pages_per_round) {
        logger(LOG_NOTICE, "Vacuum final: cannot clean %d pages, %d pages left, duration %0.3f seconds.", ($table_info->{stats}{page_count} - $to_page - $pages_per_round), $table_info->{stats}{page_count}, $vacuum_time);
      } else {
        logger(LOG_NOTICE, "Vacuum final: %d pages left, duration %.3f seconds.", ($table_info->{stats}{page_count}||0), $vacuum_time);
      }
    }

    #if (not $self->{'_no_final_analyze'}) {
    my $analyze_time = time;
    analyze($schema_name, $table_name);

    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt.");
      return -1;
    }

    $analyze_time = time - $analyze_time;
    logger(LOG_NOTICE, "Analyze final: duration %.3f second.", $analyze_time);
    #}

    my $get_stat_time = time;
    $bloat_stats = get_bloat_stats($schema_name, $table_name);
    
    if ($DBI::err) {
      logger(LOG_ERROR, "Table handling interrupt.");
      return -1;
    }
    
    $get_stat_time = time - $get_stat_time;
    logger(LOG_NOTICE,"Bloat statistics with pgstattuple: duration %.3f seconds.", $get_stat_time);

    $pages_before_vacuum = get_pages_before_vacuum($table_info->{stats}{page_count}, $expected_page_count);
  }

  my $will_be_skipped = (!$is_locked && ($is_skipped || $table_info->{stats}{page_count} < MINIMAL_COMPACT_PAGES || $bloat_stats->{free_percent} < MINIMAL_COMPACT_PERCENT));
  
  if (!$is_locked && ($print_reindex_queries || (!$no_reindex && (!$is_skipped || ($attempt == $max_retry_count) || (!$is_reindexed && $is_skipped && $attempt == 0))))) {
     
    $is_reindexed = (reindex_table($table_name, $schema_name, $db_name, $print_reindex_queries) || $is_reindexed);

    if (!$no_reindex) {
      $table_info->{stats} = get_size_stats($schema_name, $table_name);
    
      if ($DBI::err) {
        logger(LOG_ERROR, "Table handling interrupt.");
        return -1;
      }
    }
  }
    
  if (!$is_locked && !($is_skipped && !defined($is_reindexed))) {
    my $complete = (($will_be_skipped || $is_skipped) && (defined $is_reindexed ? $is_reindexed : 1));
    if ($complete) {
      logger(LOG_NOTICE, "Processing complete.");
    } else {
      logger(LOG_NOTICE, "Processing incomplete.");
    }

    if (defined $bloat_stats->{free_percent} && defined $bloat_stats->{effective_page_count} && $bloat_stats->{free_percent} > 0 && $table_info->{stats}{page_count} > $bloat_stats->{effective_page_count} && !$complete) {
      logger(LOG_WARNING, "Processing results: %d pages (%d pages including toasts and indexes), size has been reduced by %s (%s including toasts and indexes) in total. This attempt has been initially expected to compact ~%d%% more space (%d pages, %s)",
        $table_info->{stats}{page_count},
        $table_info->{stats}{total_page_count},
        nice_size($table_info->{base_stats}{size} - $table_info->{stats}{size}),
        nice_size($table_info->{base_stats}{total_size} - $table_info->{stats}{total_size}),
        $bloat_stats->{free_percent},
        $table_info->{stats}{page_count} - $bloat_stats->{effective_page_count},
        nice_size($bloat_stats->{free_space})
      );
    } else {
      logger(LOG_WARNING, "Processing results: %d pages left (%d pages including toasts and indexes), size reduced by %s (%s including toasts and indexes) in total.",
        $table_info->{stats}{page_count},
        $table_info->{stats}{total_page_count},
        nice_size($table_info->{base_stats}{size} - $table_info->{stats}{size}),
        nice_size($table_info->{base_stats}{total_size} - $table_info->{stats}{total_size}),
      );
    }
  }

  return (($is_locked || $is_skipped || $will_be_skipped) && (defined $is_reindexed ? $is_reindexed : 1));
}