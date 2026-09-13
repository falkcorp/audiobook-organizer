### Fixed

- Google Books candidates now include the volume's subtitle. Google often puts a
  franchise name in the title and the real book title in the subtitle (for example
  "Star Wars" / "A New Dawn"). Those candidates used to show up, and get scored, as
  just "Star Wars". The candidate title is now "Star Wars: A New Dawn", the subtitle
  is carried separately, and the review UI's Proposed card shows a provider's
  subtitle whenever the title doesn't already include it.
