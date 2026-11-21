### Fixed

- The Review page's regroup lane no longer shows one kind's holds under another kind's heading. A refresh started by an approve, reject or bulk action could land after you switched kind but before the new kind's own list arrived, and paint the old kind's rows until it did, or for good if the new list failed to load. Such a refresh is now dropped, and a refresh that starts after a switch asks for the kind on screen.
