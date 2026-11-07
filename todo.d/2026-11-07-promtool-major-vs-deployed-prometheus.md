- [ ] **PROMTOOL-MAJOR** The extraction-gates `dashboard-reference` job runs promtool 3.5.0,
      but the deployed Prometheus is 2.53, so `promtool check/test rules` validates rules under a
      newer parser than the one that loads them. Pin promtool to the deployed major (2.53.x) or
      upgrade the deployed Prometheus to 3.x (R1-10 review MINOR-5).
