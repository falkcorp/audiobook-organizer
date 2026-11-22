### Changed

- CI: the Repo Guards job's time limit is raised from 15 to 25 minutes. The job takes 10-14 minutes on a normal runner, so a slow runner was cancelling it after every test step had passed.
